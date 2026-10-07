package install

import (
	"context"
	"sync"

	"github.com/Ominous-Josef/dopt/internal/layout"
	"github.com/Ominous-Josef/dopt/internal/manifest"
	"github.com/Ominous-Josef/dopt/internal/registry"
	"github.com/Ominous-Josef/dopt/internal/source"
)

// State of an installed app compared with its source.
type State int

const (
	UpToDate        State = iota
	UpdateAvailable       // a different release is published
	Unknown               // can't tell, but an update can still be run
	Unavailable           // can't be updated: no recipe or no download source
)

func (s State) String() string {
	return [...]string{"up to date", "update available", "unknown", "can't update"}[s]
}

// Status is the result of checking one app for updates.
type Status struct {
	AppID     string
	Name      string
	Installed string // version recorded at install time, may be ""
	Available string // version currently published, may be ""
	State     State
	Reason    string // why the state is Unknown or Unavailable

	Recipe   manifest.Manifest
	Resolved source.Resolved
}

// Check compares one installed app with what its source publishes now, without downloading it.
func Check(ctx context.Context, l layout.Layout, appID, arch string) Status {
	st := Status{AppID: appID, Name: appID}
	e, _ := registry.Read(l.RegistryDir, appID)
	if e[registry.KeyName] != "" {
		st.Name = e[registry.KeyName]
	}
	st.Installed = e[registry.KeyVersion]

	recipe, ok, err := registry.ReadRecipe(l.RegistryDir, appID)
	switch {
	case err != nil:
		return st.unavailable(err.Error())
	case !ok:
		return st.unavailable("no saved recipe (installed before dopt 3); install it once more with -m <recipe> or -u <url>")
	case !recipe.HasDownload(arch):
		return st.unavailable("installed from a local archive; no download source")
	}
	st.Recipe = recipe

	st.Resolved, err = source.Resolve(ctx, recipe, arch)
	if err != nil {
		return st.unknown("couldn't find the download: " + err.Error())
	}
	info, err := source.Probe(ctx, st.Resolved.URL)
	if err != nil {
		return st.unknown("couldn't reach the download: " + err.Error())
	}
	st.Available = source.Version(st.Resolved, info)
	fp, reliable := source.Fingerprint(info)
	switch {
	case !reliable:
		return st.unknown("the server doesn't say which release it serves")
	case e[registry.KeyRelease] == "":
		return st.unknown("no release recorded (installed before update tracking)")
	case fp == e[registry.KeyRelease]:
		st.State = UpToDate
	default:
		st.State = UpdateAvailable
	}
	return st
}

func (s Status) unknown(reason string) Status {
	s.State, s.Reason = Unknown, reason
	return s
}

func (s Status) unavailable(reason string) Status {
	s.State, s.Reason = Unavailable, reason
	return s
}

// CheckAll checks appIDs concurrently (a few at a time), keeping their order.
func CheckAll(ctx context.Context, l layout.Layout, appIDs []string, arch string) []Status {
	out := make([]Status, len(appIDs))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, id := range appIDs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = Check(ctx, l, id, arch)
		}()
	}
	wg.Wait()
	return out
}

// UpdateRequest builds the install request that updates st's app from its saved recipe.
func UpdateRequest(l layout.Layout, st Status) Request {
	resolved := st.Resolved
	return Request{
		Manifest:     st.Recipe,
		ManifestPath: registry.RecipePath(l.RegistryDir, st.AppID),
		Download:     true,
		URL:          resolved.URL,
		Resolved:     resolved,
		Batch:        true,
	}
}
