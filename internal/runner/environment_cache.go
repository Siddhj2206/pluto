package runner

import (
	"errors"
	"path/filepath"

	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/envcache"
	"github.com/Siddhj2206/pluto/internal/state"
)

// prepareBoxDisk creates the box's rootfs from a ready environment layer when
// the box opted in and one matches, and from the base image otherwise. On a
// coordinated miss a trusted box claims the build; a concurrent box asking for
// the same missing layer gets ErrBuilding instead of provisioning a duplicate.
func (r *Runner) prepareBoxDisk(box *state.Box, imageDir, version string) error {
	boxDir := r.boxDir(box.ID)
	diskMiB := diskSizeMiB(box.Resources)
	ct, err := contract.Load(box.Worktree)
	if err != nil {
		ct = nil
	}
	key, publishable, cached := r.environmentLayerKey(box, version, ct)
	if !cached {
		return r.PrepareDisk(boxDir, imageDir, diskMiB)
	}
	layerDir, err := r.EnvironmentCache.LayerDir(key)
	switch {
	case err == nil:
		return r.PrepareLayerDisk(boxDir, layerDir, diskMiB)
	case errors.Is(err, envcache.ErrMiss):
		if !publishable {
			// An untrusted box may consume a shared secret-free layer but
			// never creates one, so it does not join build coordination.
			return r.PrepareDisk(boxDir, imageDir, diskMiB)
		}
		if !r.claimLayerBuild(key, box.ID) {
			return envcache.ErrBuilding
		}
		if err := r.PrepareDisk(boxDir, imageDir, diskMiB); err != nil {
			r.releaseLayerBuild(key, box.ID)
			return err
		}
		return nil
	default:
		return err
	}
}

// environmentLayerKey resolves the cache key for a box's declared setup. The
// third result reports whether the box opted into layer reuse at all; the
// second reports whether it may publish (only a trusted box does). An untrusted
// box resolves to the untrusted class, which only a trusted sharer populates,
// so a trusted layer is never exposed to it.
func (r *Runner) environmentLayerKey(box *state.Box, image string, ct *contract.Contract) (key string, publishable, cached bool) {
	if box == nil || ct == nil || ct.Provision == nil || !ct.Provision.Cache {
		return "", false, false
	}
	if box.PrimaryRepoURL == "" || image == "" {
		return "", false, false
	}
	trusted := boxTrusted(box)
	trust := envcache.Untrusted
	if trusted && !ct.Provision.ShareUntrusted {
		trust = envcache.Trusted
	}
	resolved, err := envcache.Key(envcache.Inputs{
		Project: box.PrimaryRepoURL,
		Setup:   ct.SetupHash(),
		Image:   image,
		Trust:   trust,
	})
	if err != nil {
		return "", false, false
	}
	return resolved, trusted, true
}

// boxTrusted resolves a box's trust class, failing closed for a pull-request
// box whose record predates the class field.
func boxTrusted(box *state.Box) bool {
	switch box.TrustClass {
	case state.TrustClassTrusted:
		return true
	case state.TrustClassUntrusted:
		return false
	default:
		return box.WorkItemType != "pull_request"
	}
}

// claimLayerBuild records this box as the single builder of a missing layer.
func (r *Runner) claimLayerBuild(key, owner string) bool {
	r.layerMu.Lock()
	defer r.layerMu.Unlock()
	if r.layerBuilds == nil {
		r.layerBuilds = make(map[string]string)
	}
	if _, ok := r.layerBuilds[key]; ok {
		return false
	}
	r.layerBuilds[key] = owner
	return true
}

// releaseLayerBuild frees a claim only for its owner, so a box that merely
// observed someone else's build never clears it.
func (r *Runner) releaseLayerBuild(key, owner string) {
	r.layerMu.Lock()
	defer r.layerMu.Unlock()
	if r.layerBuilds[key] == owner {
		delete(r.layerBuilds, key)
	}
}

// releaseBoxLayerBuild frees any claim held by this box, resolving the key from
// the box's current record. It is a no-op for a box that held no claim.
func (r *Runner) releaseBoxLayerBuild(id string) {
	box, err := r.Store.Box(id)
	if err != nil {
		return
	}
	ct, err := contract.Load(box.Worktree)
	if err != nil {
		return
	}
	key, publishable, cached := r.environmentLayerKey(box, box.Image, ct)
	if cached && publishable {
		r.releaseLayerBuild(key, id)
	}
}

// publishEnvironmentLayer snapshots a successfully provisioned, cleanly stopped
// box disk into the cache. Per-box state is scrubbed from the private copy
// before it becomes visible; a scrub or copy failure simply leaves no layer.
func (r *Runner) publishEnvironmentLayer(id, key string) {
	source := filepath.Join(r.boxDir(id), "disk", "rootfs.img")
	_ = r.EnvironmentCache.Publish(key, source, r.ScrubEnvironment)
}
