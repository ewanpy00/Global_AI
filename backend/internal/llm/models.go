package llm

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"

	"github.com/ewanpy00/Global_AI/backend/internal/config"
)

// ListModels asks a provider, through Bifrost, which model ids the configured key can use.
func (g *Gateway) ListModels(ctx context.Context, provider string) ([]string, error) {
	bctx := schemas.NewBifrostContext(ctx, time.Time{})
	bctx.SetValue(ctxInternal, true)
	var ids []string
	token := ""
	for page := 0; page < 20; page++ {
		resp, berr := g.bf.ListModelsRequest(bctx, &schemas.BifrostListModelsRequest{
			Provider: schemas.ModelProvider(provider), PageSize: 500, PageToken: token,
		})
		if berr != nil {
			return nil, BifrostErr(berr)
		}
		for _, m := range resp.Data {
			if !m.IsDeprecated {
				ids = append(ids, strings.TrimPrefix(m.ID, provider+"/"))
			}
		}
		if resp.NextPageToken == "" {
			break
		}
		token = resp.NextPageToken
	}
	sort.Strings(ids)
	return ids, nil
}

// CheckModels verifies that every routed model is offered by its provider and returns
// human-readable warnings. Free-tier line-ups change often, and a stale model id would
// otherwise only surface as an error on the first verification.
func (g *Gateway) CheckModels(ctx context.Context) []string {
	refs := []config.ModelRef{g.routes[PurposeExtract], g.routes[PurposeJudge], g.chat}
	refs = append(refs, g.fallbacks...)

	cache := map[string][]string{}
	var warnings []string
	seen := map[config.ModelRef]bool{}
	for _, ref := range refs {
		if ref.Model == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		ids, ok := cache[ref.Provider]
		if !ok {
			var err error
			ids, err = g.ListModels(ctx, ref.Provider)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("could not list %s models to check %s: %v", ref.Provider, ref, err))
				cache[ref.Provider] = nil
				continue
			}
			cache[ref.Provider] = ids
		}
		if ids == nil || contains(ids, ref.Model) {
			continue
		}
		warnings = append(warnings, fmt.Sprintf("%s is not offered by %s for this key. Available: %s",
			ref, ref.Provider, preview(ids, 12)))
	}
	return warnings
}

func contains(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func preview(ids []string, n int) string {
	if len(ids) <= n {
		return strings.Join(ids, ", ")
	}
	return strings.Join(ids[:n], ", ") + fmt.Sprintf(" … (+%d more, run `make models`)", len(ids)-n)
}
