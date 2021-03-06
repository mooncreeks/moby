package dockerfile

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/backend"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/builder"
	"github.com/docker/docker/builder/remotecontext"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/idtools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmptyDockerfile(t *testing.T) {
	contextDir, cleanup := createTestTempDir(t, "", "builder-dockerfile-test")
	defer cleanup()

	createTestTempFile(t, contextDir, builder.DefaultDockerfileName, "", 0777)

	readAndCheckDockerfile(t, "emptyDockerfile", contextDir, "", "the Dockerfile (Dockerfile) cannot be empty")
}

func TestSymlinkDockerfile(t *testing.T) {
	contextDir, cleanup := createTestTempDir(t, "", "builder-dockerfile-test")
	defer cleanup()

	createTestSymlink(t, contextDir, builder.DefaultDockerfileName, "/etc/passwd")

	// The reason the error is "Cannot locate specified Dockerfile" is because
	// in the builder, the symlink is resolved within the context, therefore
	// Dockerfile -> /etc/passwd becomes etc/passwd from the context which is
	// a nonexistent file.
	expectedError := fmt.Sprintf("Cannot locate specified Dockerfile: %s", builder.DefaultDockerfileName)

	readAndCheckDockerfile(t, "symlinkDockerfile", contextDir, builder.DefaultDockerfileName, expectedError)
}

func TestDockerfileOutsideTheBuildContext(t *testing.T) {
	contextDir, cleanup := createTestTempDir(t, "", "builder-dockerfile-test")
	defer cleanup()

	expectedError := "Forbidden path outside the build context: ../../Dockerfile ()"

	readAndCheckDockerfile(t, "DockerfileOutsideTheBuildContext", contextDir, "../../Dockerfile", expectedError)
}

func TestNonExistingDockerfile(t *testing.T) {
	contextDir, cleanup := createTestTempDir(t, "", "builder-dockerfile-test")
	defer cleanup()

	expectedError := "Cannot locate specified Dockerfile: Dockerfile"

	readAndCheckDockerfile(t, "NonExistingDockerfile", contextDir, "Dockerfile", expectedError)
}

func readAndCheckDockerfile(t *testing.T, testName, contextDir, dockerfilePath, expectedError string) {
	tarStream, err := archive.Tar(contextDir, archive.Uncompressed)
	require.NoError(t, err)

	defer func() {
		if err = tarStream.Close(); err != nil {
			t.Fatalf("Error when closing tar stream: %s", err)
		}
	}()

	if dockerfilePath == "" { // handled in BuildWithContext
		dockerfilePath = builder.DefaultDockerfileName
	}

	config := backend.BuildConfig{
		Options: &types.ImageBuildOptions{Dockerfile: dockerfilePath},
		Source:  tarStream,
	}
	_, _, err = remotecontext.Detect(config)
	assert.EqualError(t, err, expectedError)
}

func TestCopyRunConfig(t *testing.T) {
	defaultEnv := []string{"foo=1"}
	defaultCmd := []string{"old"}

	var testcases = []struct {
		doc       string
		modifiers []runConfigModifier
		expected  *container.Config
	}{
		{
			doc:       "Set the command",
			modifiers: []runConfigModifier{withCmd([]string{"new"})},
			expected: &container.Config{
				Cmd: []string{"new"},
				Env: defaultEnv,
			},
		},
		{
			doc:       "Set the command to a comment",
			modifiers: []runConfigModifier{withCmdComment("comment", runtime.GOOS)},
			expected: &container.Config{
				Cmd: append(defaultShellForOS(runtime.GOOS), "#(nop) ", "comment"),
				Env: defaultEnv,
			},
		},
		{
			doc: "Set the command and env",
			modifiers: []runConfigModifier{
				withCmd([]string{"new"}),
				withEnv([]string{"one", "two"}),
			},
			expected: &container.Config{
				Cmd: []string{"new"},
				Env: []string{"one", "two"},
			},
		},
	}

	for _, testcase := range testcases {
		runConfig := &container.Config{
			Cmd: defaultCmd,
			Env: defaultEnv,
		}
		runConfigCopy := copyRunConfig(runConfig, testcase.modifiers...)
		assert.Equal(t, testcase.expected, runConfigCopy, testcase.doc)
		// Assert the original was not modified
		assert.NotEqual(t, runConfig, runConfigCopy, testcase.doc)
	}

}

func TestChownFlagParsing(t *testing.T) {
	testFiles := map[string]string{
		"passwd": `root:x:0:0::/bin:/bin/false
bin:x:1:1::/bin:/bin/false
wwwwww:x:21:33::/bin:/bin/false
unicorn:x:1001:1002::/bin:/bin/false
		`,
		"group": `root:x:0:
bin:x:1:
wwwwww:x:33:
unicorn:x:1002:
somegrp:x:5555:
othergrp:x:6666:
		`,
	}
	// test mappings for validating use of maps
	idMaps := []idtools.IDMap{
		{
			ContainerID: 0,
			HostID:      100000,
			Size:        65536,
		},
	}
	remapped := idtools.NewIDMappingsFromMaps(idMaps, idMaps)
	unmapped := &idtools.IDMappings{}

	contextDir, cleanup := createTestTempDir(t, "", "builder-chown-parse-test")
	defer cleanup()

	if err := os.Mkdir(filepath.Join(contextDir, "etc"), 0755); err != nil {
		t.Fatalf("error creating test directory: %v", err)
	}

	for filename, content := range testFiles {
		createTestTempFile(t, filepath.Join(contextDir, "etc"), filename, content, 0644)
	}

	// positive tests
	for _, testcase := range []struct {
		name      string
		chownStr  string
		idMapping *idtools.IDMappings
		expected  idtools.IDPair
	}{
		{
			name:      "UIDNoMap",
			chownStr:  "1",
			idMapping: unmapped,
			expected:  idtools.IDPair{UID: 1, GID: 1},
		},
		{
			name:      "UIDGIDNoMap",
			chownStr:  "0:1",
			idMapping: unmapped,
			expected:  idtools.IDPair{UID: 0, GID: 1},
		},
		{
			name:      "UIDWithMap",
			chownStr:  "0",
			idMapping: remapped,
			expected:  idtools.IDPair{UID: 100000, GID: 100000},
		},
		{
			name:      "UIDGIDWithMap",
			chownStr:  "1:33",
			idMapping: remapped,
			expected:  idtools.IDPair{UID: 100001, GID: 100033},
		},
		{
			name:      "UserNoMap",
			chownStr:  "bin:5555",
			idMapping: unmapped,
			expected:  idtools.IDPair{UID: 1, GID: 5555},
		},
		{
			name:      "GroupWithMap",
			chownStr:  "0:unicorn",
			idMapping: remapped,
			expected:  idtools.IDPair{UID: 100000, GID: 101002},
		},
		{
			name:      "UserOnlyWithMap",
			chownStr:  "unicorn",
			idMapping: remapped,
			expected:  idtools.IDPair{UID: 101001, GID: 101002},
		},
	} {
		t.Run(testcase.name, func(t *testing.T) {
			idPair, err := parseChownFlag(testcase.chownStr, contextDir, testcase.idMapping)
			require.NoError(t, err, "Failed to parse chown flag: %q", testcase.chownStr)
			assert.Equal(t, testcase.expected, idPair, "chown flag mapping failure")
		})
	}

	// error tests
	for _, testcase := range []struct {
		name      string
		chownStr  string
		idMapping *idtools.IDMappings
		descr     string
	}{
		{
			name:      "BadChownFlagFormat",
			chownStr:  "bob:1:555",
			idMapping: unmapped,
			descr:     "invalid chown string format: bob:1:555",
		},
		{
			name:      "UserNoExist",
			chownStr:  "bob",
			idMapping: unmapped,
			descr:     "can't find uid for user bob: no such user: bob",
		},
		{
			name:      "GroupNoExist",
			chownStr:  "root:bob",
			idMapping: unmapped,
			descr:     "can't find gid for group bob: no such group: bob",
		},
	} {
		t.Run(testcase.name, func(t *testing.T) {
			_, err := parseChownFlag(testcase.chownStr, contextDir, testcase.idMapping)
			assert.EqualError(t, err, testcase.descr, "Expected error string doesn't match")
		})
	}
}
