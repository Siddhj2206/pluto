package runner

import (
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"
)

func TestSendCtrlAltDelCallsFirecrackerAPI(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "api.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	type action struct {
		ActionType string `json:"action_type"`
	}
	got := make(chan action, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /actions", func(w http.ResponseWriter, r *http.Request) {
		var a action
		if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		got <- a
		w.WriteHeader(http.StatusNoContent)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	if err := sendCtrlAltDel(socket); err != nil {
		t.Fatalf("sendCtrlAltDel: %v", err)
	}
	a := <-got
	if a.ActionType != "SendCtrlAltDel" {
		t.Fatalf("action = %q, want SendCtrlAltDel", a.ActionType)
	}
}

func TestSendCtrlAltDelSurfacesAPIErrors(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "api.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /actions", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such action", http.StatusBadRequest)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	if err := sendCtrlAltDel(socket); err == nil {
		t.Fatal("sendCtrlAltDel should surface a non-204 response")
	}
}
