package engine

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/spec"
	"github.com/dunkbing/coldship/internal/store"
)

// appOrg returns ctx acting in the org of app: the app's own if it exists,
// its parent's for a new preview, else the caller's (or the default org).
// An app in another org than the caller's is reported as not found, or,
// when creating one with a full spec, as a name already in use (names are
// unique across the server).
func (e *Engine) appOrg(ctx context.Context, name, previewOf string, creating bool) (context.Context, error) {
	caller := store.OrgFrom(ctx)
	a, err := e.st.GetApp(ctx, name)
	if err != nil {
		return nil, err
	}
	if a == nil && previewOf != "" {
		if a, err = e.st.GetApp(ctx, previewOf); err != nil {
			return nil, err
		}
	}
	if a != nil {
		if caller != "" && a.Org != caller {
			if creating && a.Name == name {
				return nil, apierr.New(409, "name_in_use", "the name %s is taken: app names are unique on this server", name).
					WithField("name").WithHint("pick another name")
			}
			return nil, apierr.NotFound("app", name)
		}
		return store.WithOrg(ctx, a.Org), nil
	}
	return store.WithOrg(ctx, store.OrgOr(caller)), nil
}

// MoveApp moves an app and its previews to another org. Its own databases
// go with it; an app that uses the org's data services must disconnect
// them first, so it never keeps credentials from an org it left. Pending
// plans for it expire.
func (e *Engine) MoveApp(ctx context.Context, actor, name, to string) (*store.App, error) {
	defer e.lockApp(name)()
	a, err := e.st.GetApp(ctx, name)
	if err != nil {
		return nil, err
	}
	if a == nil || (store.OrgFrom(ctx) != "" && a.Org != store.OrgFrom(ctx)) {
		return nil, apierr.NotFound("app", name)
	}
	if a.PreviewOf != "" {
		return nil, apierr.New(400, "invalid_request", "%s is a preview; move %s instead", name, a.PreviewOf)
	}
	if a.Org == to {
		return a, nil
	}
	if a.Spec != nil && len(a.Spec.Uses) > 0 {
		names := make([]string, len(a.Spec.Uses))
		for i, u := range a.Spec.Uses {
			names[i], _ = spec.ParseUse(u)
		}
		return nil, apierr.New(409, "app_uses_data", "%s uses %s from its current organization; disconnect them first", name, strings.Join(names, ", ")).
			WithDetail("uses", names).WithHint("remove them in the app's settings (Data services), then move it")
	}
	plans, err := e.st.ListPlans(ctx, store.PlanPending, 1000)
	if err != nil {
		return nil, err
	}
	for i := range plans {
		if p := &plans[i]; p.App == name || strings.HasPrefix(p.App, name+"-") {
			if pa, _ := e.st.GetApp(ctx, p.App); p.App == name || (pa != nil && pa.PreviewOf == name) {
				p.Status, p.NextStep = store.PlanExpired, "The app moved to another organization; make a new plan."
				if err := e.st.SavePlan(ctx, p); err != nil {
					return nil, err
				}
			}
		}
	}
	from := a.Org
	if err := e.st.MoveApp(ctx, name, to); err != nil {
		return nil, err
	}
	detail := map[string]any{"from": from, "to": to}
	e.st.AddAudit(context.WithoutCancel(ctx), store.AuditEntry{Time: e.Now(), Actor: actor, Action: "app.move", Target: name, Org: from, Detail: mustJSON(detail)})
	e.st.AddAudit(context.WithoutCancel(ctx), store.AuditEntry{Time: e.Now(), Actor: actor, Action: "app.move", Target: name, Org: to, Detail: mustJSON(detail)})
	a.Org = to
	return a, nil
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
