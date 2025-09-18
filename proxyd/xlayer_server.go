package proxyd

import (
	"net/http"
	"regexp"

	"github.com/ethereum/go-ethereum/log"
)

func (s *Server) HandleManySegsWS(w http.ResponseWriter, r *http.Request) {
	// >2 segments (/{...}/{...}) we check.
	if matched, _ := regexp.MatchString("^/[a-zA-Z0-9_-]+(?:/[a-zA-Z0-9_-]+)+$", r.URL.Path); !matched {
		log.Warn("invalid WS path rejected", "path", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Call the original HandleWS if validation passes
	s.HandleWS(w, r)
}
