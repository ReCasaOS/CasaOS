package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Container struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	Status  string            `json:"status"`
	Labels  map[string]string `json:"labels"`
	Project string            `json:"project"`
}

type Network struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
}

type Volume struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
}

type snapshot struct {
	Containers map[string]Container `json:"containers"`
	Networks   map[string]Network   `json:"networks"`
	Volumes    map[string]Volume    `json:"volumes"`
	Seq        int                  `json:"seq"`
}

type Store struct {
	mu         sync.Mutex
	dir        string
	containers map[string]Container
	networks   map[string]Network
	volumes    map[string]Volume
	seq        int
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	st := &Store{
		dir:        dir,
		containers: map[string]Container{},
		networks:   map[string]Network{},
		volumes:    map[string]Volume{},
	}
	raw, err := os.ReadFile(st.path())
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return nil, err
	}
	var snap snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	if snap.Containers != nil {
		st.containers = snap.Containers
	}
	if snap.Networks != nil {
		st.networks = snap.Networks
	}
	if snap.Volumes != nil {
		st.volumes = snap.Volumes
	}
	st.seq = snap.Seq
	return st, nil
}

func (s *Store) path() string { return filepath.Join(s.dir, "state.json") }

func (s *Store) saveLocked() error {
	snap := snapshot{Containers: s.containers, Networks: s.networks, Volumes: s.volumes, Seq: s.seq}
	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(), raw, 0o600)
}

func (s *Store) nextIDLocked() string {
	s.seq++
	return fmt.Sprintf("%012d", s.seq)
}

func (s *Store) findContainerLocked(key string) (Container, bool) {
	if c, ok := s.containers[key]; ok {
		return c, true
	}
	for _, c := range s.containers {
		if c.Name == key {
			return c, true
		}
	}
	var matches []Container
	for _, c := range s.containers {
		if strings.HasPrefix(c.ID, key) {
			matches = append(matches, c)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return Container{}, false
}

func (s *Store) CreateContainer(name, image string, labels map[string]string) (Container, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextIDLocked()
	if name == "" {
		name = id
	}
	if labels == nil {
		labels = map[string]string{}
	}
	if err := s.copyComposeYAMLLocked(labels); err != nil {
		return Container{}, err
	}
	c := Container{
		ID:      id,
		Name:    name,
		Image:   image,
		Status:  "created",
		Labels:  labels,
		Project: labels["com.docker.compose.project"],
	}
	s.containers[id] = c
	if err := s.saveLocked(); err != nil {
		delete(s.containers, id)
		return Container{}, err
	}
	return c, nil
}

func (s *Store) Start(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.findContainerLocked(id)
	if !ok {
		return fmt.Errorf("no such container")
	}
	c.Status = "running"
	s.containers[c.ID] = c
	return s.saveLocked()
}

func (s *Store) Stop(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.findContainerLocked(id)
	if !ok {
		return fmt.Errorf("no such container")
	}
	c.Status = "exited"
	s.containers[c.ID] = c
	return s.saveLocked()
}

func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.findContainerLocked(id)
	if !ok {
		return fmt.Errorf("no such container")
	}
	delete(s.containers, c.ID)
	return s.saveLocked()
}

func (s *Store) Get(id string) (Container, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.findContainerLocked(id)
}

func (s *Store) List() []Container {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Container, 0, len(s.containers))
	for _, c := range s.containers {
		out = append(out, c)
	}
	return out
}

func (s *Store) copyComposeYAMLLocked(labels map[string]string) error {
	project := labels["com.docker.compose.project"]
	configFiles := labels["com.docker.compose.project.config_files"]
	if project == "" || configFiles == "" {
		return nil
	}
	src := strings.Split(configFiles, ",")[0]
	src = strings.TrimSpace(src)
	if src == "" {
		return nil
	}
	destDir := filepath.Join(s.dir, project)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(filepath.Join(destDir, "docker-compose.yml"))
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func (s *Store) CreateNetwork(name string, labels map[string]string) (Network, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextIDLocked()
	if labels == nil {
		labels = map[string]string{}
	}
	n := Network{ID: id, Name: name, Labels: labels}
	s.networks[id] = n
	if err := s.saveLocked(); err != nil {
		return Network{}, err
	}
	return n, nil
}

func (s *Store) findNetworkLocked(key string) (Network, bool) {
	if n, ok := s.networks[key]; ok {
		return n, true
	}
	for _, n := range s.networks {
		if n.Name == key {
			return n, true
		}
	}
	var matches []Network
	for _, n := range s.networks {
		if strings.HasPrefix(n.ID, key) {
			matches = append(matches, n)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return Network{}, false
}

func (s *Store) GetNetwork(id string) (Network, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.findNetworkLocked(id)
}

func (s *Store) ListNetworks() []Network {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Network, 0, len(s.networks))
	for _, n := range s.networks {
		out = append(out, n)
	}
	return out
}

func (s *Store) RemoveNetwork(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.findNetworkLocked(id)
	if !ok {
		return fmt.Errorf("no such network")
	}
	delete(s.networks, n.ID)
	return s.saveLocked()
}

func (s *Store) CreateVolume(name string, labels map[string]string) (Volume, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if labels == nil {
		labels = map[string]string{}
	}
	v := Volume{Name: name, Labels: labels}
	s.volumes[name] = v
	if err := s.saveLocked(); err != nil {
		return Volume{}, err
	}
	return v, nil
}

func (s *Store) GetVolume(name string) (Volume, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.volumes[name]
	return v, ok
}

func (s *Store) ListVolumes() []Volume {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Volume, 0, len(s.volumes))
	for _, v := range s.volumes {
		out = append(out, v)
	}
	return out
}

func (s *Store) RemoveVolume(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.volumes[name]; !ok {
		return fmt.Errorf("no such volume")
	}
	delete(s.volumes, name)
	return s.saveLocked()
}
