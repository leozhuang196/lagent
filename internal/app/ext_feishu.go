package app

import (
	"context"
	"time"

	connfeishu "github.com/eternityx/lagent/internal/capability/connector/feishu"
	"github.com/eternityx/lagent/internal/infra/queue"
	orchfeishu "github.com/eternityx/lagent/internal/orch/feishu"
	transfeishu "github.com/eternityx/lagent/internal/transport/feishu"
	"github.com/eternityx/lagent/internal/transport/httpapi/feishuapi"
)

// artifactLinkTTL is how long download links in Feishu replies stay valid.
const artifactLinkTTL = 7 * 24 * time.Hour

// The Feishu extension (§9.4, §9.5, §9.18). Without an app id only the
// binding routes are mounted, so admins can prepare bindings before the app
// exists. With one, the connector tools, the turn hook and the queue handlers
// are registered, and processes with the feishu role hold the long connection
// (Feishu limits connections per app, so no other role opens one).
func init() {
	RegisterExtension(Extension{Name: "feishu", Build: func(a *App) error {
		svc := &orchfeishu.Service{
			Store: a.Infra.Store, Sessions: a.Sessions, Queue: a.Infra.Queue, Counter: a.Infra.Coord,
			Clock: a.Clock, Log: a.Log, WebBaseURL: a.Cfg.HTTP.BaseURL,
		}
		a.HTTP.Mount(feishuapi.Routes(svc))
		cfg := a.Cfg.Feishu
		if cfg.AppID == "" {
			return nil
		}
		client := transfeishu.NewLarkClient(cfg.AppID, cfg.AppSecret, cfg.Domain)
		svc.Client = client
		svc.Links = orchfeishu.LinksFunc(func(ctx context.Context, sessionID, turnID string) ([]orchfeishu.Link, error) {
			arts, err := a.Sessions.ListArtifacts(ctx, sessionID, "")
			if err != nil {
				return nil, err
			}
			var out []orchfeishu.Link
			for _, art := range arts {
				if art.TurnID != turnID {
					continue
				}
				u, err := a.Infra.Objects.SignedURL(ctx, art.ObjectKey, artifactLinkTTL)
				if err != nil {
					return out, err
				}
				out = append(out, orchfeishu.Link{Name: art.Name, URL: u})
			}
			return out, nil
		})
		a.Registry.Register(connfeishu.Tools(client, connfeishu.StoreBindings{Store: a.Infra.Store})...)
		a.AddHook(svc)
		if a.Cfg.Has(RoleWorker) {
			a.Handle(queue.TypeFeishuInbound, svc.HandleInbound)
			a.Handle(queue.TypeFeishuDeliver, svc.HandleDeliver)
		}
		if a.Cfg.Has(RoleFeishu) {
			gw := &transfeishu.Gateway{AppID: cfg.AppID, AppSecret: cfg.AppSecret, Domain: cfg.Domain, Ingest: svc, Log: a.Log}
			a.OnStart(gw.Run)
		}
		return nil
	}})
}
