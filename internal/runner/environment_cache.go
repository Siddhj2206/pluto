package runner

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/envcache"
	"github.com/Siddhj2206/pluto/internal/state"
)

// prepareBoxDisk creates the box's rootfs from a ready environment layer when
// the box opted in and one matches, and from the base image otherwise. On a
// coordinated miss a trusted box claims the build; a concurrent box asking for
// the same missing layer gets ErrBuilding instead of provisioning a duplicate.
//
// The layer identity (key and publishability) is resolved once, from the live
// contract, when the box's disk is first created, and persisted on the box
// record. A later contract edit cannot move the key out from under the disk:
// publish and release read the persisted identity back.
func (r *Runner) prepareBoxDisk(box *state.Box, imageDir, version string) error {
	boxDir := r.boxDir(box.ID)
	diskMiB := diskSizeMiB(box.Resources)
	if box.EnvironmentLayer != nil {
		return r.prepareFromLayer(box, box.EnvironmentLayer, boxDir, imageDir, diskMiB)
	}
	if diskExists(boxDir) {
		// The disk predates layer caching, or the contract opted in after the
		// disk existed. Either way its setup is frozen; a later edit must not
		// retroactively enroll an existing disk in the cache.
		return r.PrepareDisk(boxDir, imageDir, diskMiB)
	}
	ct, err := contract.Load(box.Worktree)
	if err != nil {
		ct = nil
	}
	key, publishable, cached := r.environmentLayerKey(box, version, ct)
	if !cached {
		return r.PrepareDisk(boxDir, imageDir, diskMiB)
	}
	layer := &state.EnvironmentLayer{Key: key, Publishable: publishable}
	if _, err := r.Store.SetEnvironmentLayer(box.ID, layer); err != nil {
		return err
	}
	box.EnvironmentLayer = layer
	return r.prepareFromLayer(box, layer, boxDir, imageDir, diskMiB)
}

// prepareFromLayer materializes a box disk for a persisted layer identity: a
// cache hit clones the published layer, a miss lets a publishable box claim
// the build and clone the base image, and an unpublishable box simply clones
// the base image without joining coordination.
func (r *Runner) prepareFromLayer(box *state.Box, layer *state.EnvironmentLayer, boxDir, imageDir string, diskMiB int) error {
	layerDir, err := r.EnvironmentCache.LayerDir(layer.Key)
	switch {
	case err == nil:
		return r.PrepareLayerDisk(boxDir, layerDir, diskMiB)
	case errors.Is(err, envcache.ErrMiss):
		if !layer.Publishable {
			// An untrusted box may consume a shared secret-free layer but
			// never creates one, so it does not join build coordination.
			return r.PrepareDisk(boxDir, imageDir, diskMiB)
		}
		if !r.claimLayerBuild(layer.Key, box.ID) {
			return envcache.ErrBuilding
		}
		if err := r.PrepareDisk(boxDir, imageDir, diskMiB); err != nil {
			r.releaseLayerBuild(layer.Key, box.ID)
			return err
		}
		return nil
	default:
		return err
	}
}

// diskExists reports whether a box already has a rootfs on the host.
func diskExists(boxDir string) bool {
	_, err := os.Stat(filepath.Join(boxDir, "disk", "rootfs.img"))
	return err == nil
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

// layerClaim is one in-memory build claim: the box that holds it and when it
// was taken, so a stale claim can be taken over.
type layerClaim struct {
	owner string
	taken time.Time
}

// claimLayerBuild records this box as the builder of a missing layer. A claim
// is not held forever: once it is older than the lease, another box may take
// it over, so a builder that will never publish (auto-pause off, or a
// provision that never finishes) cannot starve its peers. A rare takeover can
// duplicate one build; that is the accepted cost of not deadlocking.
func (r *Runner) claimLayerBuild(key, owner string) bool {
	r.layerMu.Lock()
	defer r.layerMu.Unlock()
	if r.layerBuilds == nil {
		r.layerBuilds = make(map[string]layerClaim)
	}
	if claim, ok := r.layerBuilds[key]; ok && claim.owner != owner && r.now().Sub(claim.taken) < r.layerClaimLease() {
		return false
	}
	r.layerBuilds[key] = layerClaim{owner: owner, taken: r.now()}
	return true
}

// releaseLayerBuild frees a claim only for its owner, so a box that merely
// observed someone else's build never clears it.
func (r *Runner) releaseLayerBuild(key, owner string) {
	r.layerMu.Lock()
	defer r.layerMu.Unlock()
	if claim, ok := r.layerBuilds[key]; ok && claim.owner == owner {
		delete(r.layerBuilds, key)
	}
}

// releaseBoxLayerBuild frees any claim held by this box, using the layer
// identity persisted when the box's disk was created. It is a no-op for a box
// that held no claim or has no persisted layer.
func (r *Runner) releaseBoxLayerBuild(id string) {
	box, err := r.Store.Box(id)
	if err != nil {
		return
	}
	if box.EnvironmentLayer == nil {
		return
	}
	r.releaseLayerBuild(box.EnvironmentLayer.Key, id)
}

// publishEnvironmentLayer snapshots a successfully provisioned, cleanly stopped
// box disk into the cache. Per-box state is scrubbed from the private copy
// before it becomes visible; a scrub or copy failure simply leaves no layer.
func (r *Runner) publishEnvironmentLayer(id, key string) {
	source := filepath.Join(r.boxDir(id), "disk", "rootfs.img")
	_ = r.EnvironmentCache.Publish(key, source, r.ScrubEnvironment)
}
