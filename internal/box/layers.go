package box

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/shquote"
	"github.com/Siddhj2206/pluto/internal/state"
)

// boxLayers compiles a workspace's provision into the content-addressed layers
// a new box is seeded from: the base, then, when provision declares one, the
// package layer on top of it. With no provision it falls back to the requested
// image (or the default) as a single base layer.
//
// Both layers are content-addressed by provision's declaration, so an
// unchanged declaration resolves from the engine's cache and a changed one
// builds only the layer whose content moved.
func (m *Manager) boxLayers(ctx context.Context, project *contract.Project, workspace, image string) ([]container.LayerRef, error) {
	if project.Provision == nil {
		if image == "" {
			image = DefaultImage
		}
		ref, err := m.resolveLayer(ctx, container.LayerSpec{Kind: container.LayerBase, Source: image})
		if err != nil {
			return nil, err
		}
		return []container.LayerRef{ref}, nil
	}

	baseSpec, err := baseLayerSpec(project.Provision, workspace)
	if err != nil {
		return nil, err
	}
	base, err := m.resolveLayer(ctx, baseSpec)
	if err != nil {
		return nil, err
	}
	layers := []container.LayerRef{base}
	if !project.Provision.HasPackageLayer() {
		return layers, nil
	}
	pkgSpec, err := packageLayerSpec(project.Provision, workspace, base)
	if err != nil {
		return nil, err
	}
	pkg, err := m.resolveLayer(ctx, pkgSpec)
	if err != nil {
		return nil, err
	}
	return append(layers, pkg), nil
}

// baseLayerSpec describes provision.base as an engine layer: an OCI reference
// to pull, or a Dockerfile or inline steps to build into a content-addressed
// image.
func baseLayerSpec(pv *contract.Provision, workspace string) (container.LayerSpec, error) {
	switch pv.Base.Kind {
	case contract.BaseDockerfile:
		path := pv.Base.Dockerfile
		if !filepath.IsAbs(path) {
			path = filepath.Join(workspace, path)
		}
		digest, err := pv.BaseDigest(workspace)
		if err != nil {
			return container.LayerSpec{}, err
		}
		return container.LayerSpec{
			Kind:   container.LayerBase,
			Digest: digest,
			Build:  &container.LayerBuild{Dockerfile: path, Context: filepath.Dir(path)},
		}, nil
	case contract.BaseRun:
		digest, err := pv.BaseDigest(workspace)
		if err != nil {
			return container.LayerSpec{}, err
		}
		return container.LayerSpec{
			Kind:   container.LayerBase,
			Digest: digest,
			Build:  &container.LayerBuild{From: DefaultImage, Run: []string{pv.Base.Run.String()}},
		}, nil
	default:
		return container.LayerSpec{Kind: container.LayerBase, Source: pv.Base.Image}, nil
	}
}

// packageLayerSpec describes the package layer that sits on the base: the
// declared packages are installed, then the inline steps run. Its digest binds
// the base's resolved key and the watched lockfiles.
func packageLayerSpec(pv *contract.Provision, workspace string, base container.LayerRef) (container.LayerSpec, error) {
	digest, err := pv.PackageDigest(workspace, base.Key)
	if err != nil {
		return container.LayerSpec{}, err
	}
	steps := make([]string, 0, 2)
	if len(pv.Packages) > 0 {
		steps = append(steps, aptInstallCommand(pv.Packages))
	}
	if !pv.Run.IsZero() {
		steps = append(steps, pv.Run.String())
	}
	return container.LayerSpec{
		Kind:   container.LayerPackage,
		Parent: base.Key,
		Digest: digest,
		Build:  &container.LayerBuild{From: base.Key, Run: steps},
	}, nil
}

// aptInstallCommand renders the install step for a package layer. A layer
// build runs as root, so no privilege escalation is needed.
func aptInstallCommand(packages []string) string {
	quoted := make([]string, len(packages))
	for i, p := range packages {
		quoted[i] = shquote.Quote(p)
	}
	return "apt-get update && apt-get install -y --no-install-recommends " + strings.Join(quoted, " ")
}

// seedImage is the image a container boots from: the topmost layer's key,
// which is a runnable image reference for the real engine.
func seedImage(layers []container.LayerRef) string {
	if len(layers) == 0 {
		return DefaultImage
	}
	return layers[len(layers)-1].Key
}

// layerRecords maps resolved layers to the box record's layers.
func layerRecords(layers []container.LayerRef) []state.Layer {
	out := make([]state.Layer, len(layers))
	for i, l := range layers {
		out[i] = state.Layer{Kind: l.Kind, Key: l.Key}
	}
	return out
}
