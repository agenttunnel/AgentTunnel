package contracts

// ThreadOwner records who started a thread: the user directly, an
// orchestrator acting on their behalf (a subthread), or the home-screen
// master chat (a durable master conversation). It is set only at creation
// and never changes.
type ThreadOwner string

const (
	ThreadOwnerUser         ThreadOwner = "user"
	ThreadOwnerOrchestrator ThreadOwner = "orchestrator"
	ThreadOwnerMaster       ThreadOwner = "master"
)

// Normalized gives the owner an explicit value: logs recorded before owners
// existed carry an empty owner, which reads as the user.
func (o ThreadOwner) Normalized() ThreadOwner {
	if o == "" {
		return ThreadOwnerUser
	}
	return o
}
