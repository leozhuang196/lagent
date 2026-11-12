package app

import (
	"github.com/go-chi/chi/v5"

	"github.com/eternityx/lagent/internal/transport/httpapi/adminapi"
)

func init() {
	RegisterExtension(Extension{Name: "admin", Build: func(a *App) error {
		// a.Admin exists before extensions run (it is the key lookup of the
		// auth middleware); here it gets the collaborators the console needs.
		ad := a.Admin
		ad.Registry = a.Registry
		ad.Sessions = a.Sessions
		ad.Counter = a.Infra.Coord
		ad.Bus = a.Infra.Bus
		ad.Info.StoreDriver = a.Cfg.Store.Driver
		ad.Info.Redis = a.Cfg.Redis.Addr != ""
		ad.Info.DefaultModel = a.Cfg.LLM.Default
		ad.Info.QueueDriver = "memory"
		if ad.Info.Redis {
			ad.Info.QueueDriver = "redis"
		}
		ad.Info.ProcessRoles = nil
		for _, r := range a.Cfg.Roles {
			ad.Info.ProcessRoles = append(ad.Info.ProcessRoles, string(r))
		}
		a.HTTP.Mount(func(r chi.Router) { adminapi.Mount(r, ad) })
		return nil
	}})
}
