package conductor

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A repository arrives escaped from repoArg and is split back into its plain parts.
func TestSplitRepoUnescapesOwnerAndName(t *testing.T) {
	repo, err := repoArg(map[string]any{"repo": " acme/api.v2/ "})
	assert.NoError(t, err)
	owner, name := splitRepo(repo)
	assert.Equal(t, "acme", owner)
	assert.Equal(t, "api.v2", name)
}

// The reader lists repositories first, so a routine can turn words into owner/name.
func TestTheReaderListsRepositories(t *testing.T) {
	assert.Equal(t, "list_repositories", githubReadTools[0].Name)
}
