package contracts

// WorktreeRef is the git worktree a thread's agents work in: a checkout of a
// fresh branch, kept beside the project rather than inside it. The thread
// carries the ref, so every workdir resolver can find it from the event log.
type WorktreeRef struct {
	// Path is the worktree directory; Branch is the branch checked out there and
	// Base is the branch it was cut from.
	Path   string `json:"path"`
	Branch string `json:"branch"`
	Base   string `json:"base"`
	// Inherited marks a subthread's view of its parent's worktree. It shares the
	// directory but never owns it, so it is never removed with the subthread.
	Inherited bool `json:"inherited,omitempty"`
}
