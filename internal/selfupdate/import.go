package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// importCandidate copies untrusted bytes into privileged private staging before
// any executable can be activated. Stager re-verifies metadata and archive/raw
// digests there; no service-owned executable path is ever executed as privileged.
func importCandidate(ctx context.Context, paths nativePaths, trust Trust, installed Installed, request ActivationRequest) (*os.Root, Installed, error) {
	sourcePath, err := StageDirectory(filepath.Join(paths.Data, "selfupdate", "stages"), request.StageID)
	if err != nil {
		return nil, Installed{}, err
	}
	source, err := safepath.OpenRoot(ctx, sourcePath)
	if err != nil {
		return nil, Installed{}, err
	}
	defer source.Close()
	metadata, err := readChecked(ctx, source, "release.json", MaxManifestBytes)
	if err != nil {
		return nil, Installed{}, err
	}
	sig, err := readChecked(ctx, source, "release.sig", 1024)
	if err != nil {
		return nil, Installed{}, err
	}
	var untrusted Manifest
	if strictJSON(metadata, &untrusted) != nil {
		return nil, Installed{}, errors.New("invalid staged metadata")
	}
	manifest, err := VerifyManifest(trust, metadata, sig, untrusted.Version, installed.Version, installed.Schema, time.Now())
	if err != nil {
		return nil, Installed{}, err
	}
	artifact, err := manifest.Artifact(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return nil, Installed{}, err
	}
	archive, err := source.Open(artifact.Name)
	if err != nil {
		return nil, Installed{}, err
	}
	defer archive.Close()
	stagesPath := filepath.Join(paths.State, "stages")
	stager := Stager{Dir: stagesPath, Trust: trust, CurrentVersion: installed.Version, Schema: installed.Schema, OS: runtime.GOOS, Arch: runtime.GOARCH}
	staged, err := stager.Stage(ctx, manifest.Version, metadata, sig, archive)
	if err != nil {
		return nil, Installed{}, err
	}
	if staged.ID != request.StageID {
		return nil, Installed{}, errors.New("stage identity differs from native request")
	}
	candidate, err := safepath.OpenRoot(ctx, filepath.Join(stagesPath, staged.ID))
	return candidate, Installed{Format: 1, Version: manifest.Version, Schema: manifest.SchemaAfter, SHA256: artifact.BinarySHA256}, err
}
func readInstalled(ctx context.Context, state *os.Root) (Installed, error) {
	var installed Installed
	if err := protectedFile(state, "installed.json"); err != nil {
		return installed, err
	}
	body, err := readChecked(ctx, state, "installed.json", 2048)
	if err != nil {
		return installed, err
	}
	if strictJSON(body, &installed) != nil || installed.Format != 1 || !validHash(installed.SHA256) || installed.Schema < 1 {
		return installed, errors.New("installed manager receipt is invalid")
	}
	if _, err := ParseStable(installed.Version); err != nil {
		return installed, err
	}
	return installed, nil
}
func copyRequest(ctx context.Context, paths nativePaths, state *os.Root, id string) (ActivationRequest, error) {
	var request ActivationRequest
	control, err := safepath.OpenRoot(ctx, controlDirectory(paths.Data))
	if err != nil {
		return request, err
	}
	defer control.Close()
	body, err := readChecked(ctx, control, "request.json", 2048)
	if err != nil {
		return request, err
	}
	if strictJSON(body, &request) != nil || request.StageID != id || !validHash(request.StageID) || request.ActorID < 1 || request.RequestedAt.After(time.Now()) || time.Since(request.RequestedAt) > time.Minute {
		return request, errors.New("invalid native update request")
	}
	if err := writeFile(ctx, state, "request.json", bytesReader(body), int64(len(body)), ""); err != nil {
		return request, err
	}
	return request, syncPrivilegedRoot(ctx, state)
}
func readRequest(ctx context.Context, state *os.Root) (ActivationRequest, error) {
	var request ActivationRequest
	body, err := readChecked(ctx, state, "request.json", 2048)
	if err != nil {
		return request, err
	}
	if json.Unmarshal(body, &request) != nil || !validHash(request.StageID) || request.ActorID < 1 || request.RequestedAt.After(time.Now()) || time.Since(request.RequestedAt) > 5*time.Minute {
		return request, errors.New("imported native request is invalid or expired")
	}
	return request, nil
}
