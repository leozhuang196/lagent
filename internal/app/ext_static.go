package app

import "github.com/eternityx/lagent/internal/transport/static"

func init() {
	RegisterExtension(Extension{Name: "static", Build: func(a *App) error {
		// Everything that is not an API route serves the web UI.
		a.HTTP.Router().NotFound(static.Handler(a.Cfg.HTTP.StaticDir).ServeHTTP)
		return nil
	}})
}
