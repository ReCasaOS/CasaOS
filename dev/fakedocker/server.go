package main

import (
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
)

func Listen(socket string) (net.Listener, error) {
	_ = os.Remove(socket)
	return net.Listen("unix", socket)
}

func Serve(l net.Listener, st *Store) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/_ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("API-Version", "1.44")
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("OK"))
	})
	h := &engine{st: st}
	mux.Handle("/", h)
	return http.Serve(l, mux)
}

type engine struct {
	st *Store
}

func stripVersion(path string) string {
	p := strings.TrimPrefix(path, "/")
	if strings.HasPrefix(p, "v1.") {
		if i := strings.Index(p, "/"); i >= 0 {
			return p[i:]
		}
		return "/"
	}
	return "/" + p
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeNotFound(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusNotFound, map[string]string{"message": msg})
}

func (e *engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := stripVersion(r.URL.Path)
	if r.Method == http.MethodGet && (path == "/version" || r.URL.Path == "/version") {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"Version":    "27.0.0-fakedocker",
			"ApiVersion": "1.44",
			"Os":         "linux",
			"Arch":       "amd64",
		})
		return
	}

	switch {
	case r.Method == http.MethodGet && path == "/info":
		writeJSON(w, http.StatusOK, map[string]any{
			"ID":            "fakedocker",
			"NCPU":          1,
			"MemTotal":      1,
			"OSType":        "linux",
			"Architecture":  "x86_64",
			"ServerVersion": "27.0.0-fakedocker",
		})
		return

	case r.Method == http.MethodGet && path == "/containers/json":
		list := e.st.List()
		out := make([]map[string]any, 0, len(list))
		for _, c := range list {
			out = append(out, map[string]any{
				"Id":     c.ID,
				"Names":  []string{"/" + c.Name},
				"Image":  c.Image,
				"State":  c.Status,
				"Status": c.Status,
				"Labels": c.Labels,
			})
		}
		writeJSON(w, http.StatusOK, out)
		return

	case r.Method == http.MethodPost && path == "/containers/create":
		var body struct {
			Image  string            `json:"Image"`
			Labels map[string]string `json:"Labels"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
			return
		}
		name := r.URL.Query().Get("name")
		c, err := e.st.CreateContainer(name, body.Image, body.Labels)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"Id": c.ID, "Warnings": []string{}})
		return

	case r.Method == http.MethodGet && strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/json"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/json")
		c, ok := e.st.Get(id)
		if !ok {
			writeNotFound(w, "No such container: "+id)
			return
		}
		running := c.Status == "running"
		writeJSON(w, http.StatusOK, map[string]any{
			"Id":   c.ID,
			"Name": c.Name,
			"State": map[string]any{
				"Status":  c.Status,
				"Running": running,
			},
			"Config": map[string]any{
				"Image":  c.Image,
				"Labels": c.Labels,
			},
			"HostConfig":       map[string]any{},
			"NetworkSettings": map[string]any{},
		})
		return

	case r.Method == http.MethodPost && strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/start"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/start")
		if err := e.st.Start(id); err != nil {
			writeNotFound(w, "No such container: "+id)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return

	case r.Method == http.MethodPost && strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/stop"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/stop")
		if err := e.st.Stop(id); err != nil {
			writeNotFound(w, "No such container: "+id)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return

	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/containers/"):
		id := strings.TrimPrefix(path, "/containers/")
		if i := strings.Index(id, "/"); i >= 0 {
			id = id[:i]
		}
		if err := e.st.Remove(id); err != nil {
			writeNotFound(w, "No such container: "+id)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return

	case r.Method == http.MethodPost && strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/wait"):
		<-r.Context().Done()
		writeJSON(w, http.StatusOK, map[string]int{"StatusCode": 0})
		return

	case r.Method == http.MethodGet && strings.HasPrefix(path, "/images/") && strings.HasSuffix(path, "/json"):
		name := strings.TrimPrefix(path, "/images/")
		name = strings.TrimSuffix(name, "/json")
		writeJSON(w, http.StatusOK, map[string]any{
			"Id":       "sha256:" + name,
			"RepoTags": []string{name},
		})
		return

	case r.Method == http.MethodPost && path == "/images/create":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"status":"Pull complete"}`+"\n")
		return

	case r.Method == http.MethodGet && path == "/networks":
		list := e.st.ListNetworks()
		out := make([]map[string]any, 0, len(list))
		for _, n := range list {
			out = append(out, map[string]any{
				"Id":     n.ID,
				"Name":   n.Name,
				"Labels": n.Labels,
			})
		}
		writeJSON(w, http.StatusOK, out)
		return

	case r.Method == http.MethodPost && path == "/networks/create":
		var body struct {
			Name   string            `json:"Name"`
			Labels map[string]string `json:"Labels"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
			return
		}
		n, err := e.st.CreateNetwork(body.Name, body.Labels)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"Id": n.ID, "Warning": ""})
		return

	case r.Method == http.MethodGet && strings.HasPrefix(path, "/networks/"):
		id := strings.TrimPrefix(path, "/networks/")
		n, ok := e.st.GetNetwork(id)
		if !ok {
			writeNotFound(w, "No such network: "+id)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"Id":         n.ID,
			"Name":       n.Name,
			"Labels":     n.Labels,
			"Containers": map[string]any{},
		})
		return

	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/networks/"):
		id := strings.TrimPrefix(path, "/networks/")
		if err := e.st.RemoveNetwork(id); err != nil {
			writeNotFound(w, "No such network: "+id)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return

	case r.Method == http.MethodGet && path == "/volumes":
		list := e.st.ListVolumes()
		vols := make([]map[string]any, 0, len(list))
		for _, v := range list {
			vols = append(vols, map[string]any{
				"Name":   v.Name,
				"Labels": v.Labels,
				"Driver": "local",
				"Scope":  "local",
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"Volumes": vols, "Warnings": []string{}})
		return

	case r.Method == http.MethodGet && strings.HasPrefix(path, "/volumes/"):
		name := strings.TrimPrefix(path, "/volumes/")
		v, ok := e.st.GetVolume(name)
		if !ok {
			writeNotFound(w, "No such volume: "+name)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"Name":   v.Name,
			"Labels": v.Labels,
		})
		return

	case r.Method == http.MethodPost && path == "/volumes/create":
		var body struct {
			Name   string            `json:"Name"`
			Labels map[string]string `json:"Labels"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
			return
		}
		v, err := e.st.CreateVolume(body.Name, body.Labels)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"Name": v.Name, "Labels": v.Labels})
		return

	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/volumes/"):
		name := strings.TrimPrefix(path, "/volumes/")
		if err := e.st.RemoveVolume(name); err != nil {
			writeNotFound(w, "No such volume: "+name)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return

	case r.Method == http.MethodGet && path == "/events":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		<-r.Context().Done()
		return
	}

	log.Printf("%s %s", r.Method, r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": "not implemented: " + r.Method + " " + r.URL.Path})
}
