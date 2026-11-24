package gogit

import "github.com/go-git/go-git/v5/plumbing"

// ValidBranchName proves a positive result. Git decides rejected names because
// its full-ref grammar permits components such as "@" that go-git rejects.
func ValidBranchName(branch string) bool {
	return plumbing.NewBranchReferenceName(branch).Validate() == nil
}
