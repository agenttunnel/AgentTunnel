package domain

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func taskAdd(name, title string) contracts.Command {
	return contracts.Command{Type: contracts.CmdTaskAdd, ThreadID: "t1", TaskAdd: &contracts.TaskAdded{Name: name, Title: title}}
}

func taskStatus(name string, status contracts.TaskStatus) contracts.Command {
	return contracts.Command{Type: contracts.CmdTaskStatusSet, ThreadID: "t1", TaskStatus: &contracts.TaskStatusChanged{Name: name, Status: status}}
}

func existingThread() *ThreadState {
	s := NewThreadState("t1")
	s.Exists = true
	return s
}

func TestDecideTaskAddRejectsBadInput(t *testing.T) {
	s := existingThread()
	for _, cmd := range []contracts.Command{
		{Type: contracts.CmdTaskAdd, ThreadID: "t1"},
		taskAdd("  ", "a title"),
		taskAdd("api", "   "),
	} {
		if _, err := Decide(cmd, s); err == nil {
			t.Fatalf("task add %+v should be rejected", cmd.TaskAdd)
		}
	}
}

func TestDecideTaskAddRejectsDuplicateNames(t *testing.T) {
	s := existingThread()
	s.Tasks = []TaskItem{{Name: "api", Title: "Design the API", Status: contracts.TaskTodo}}
	if _, err := Decide(taskAdd("api", "Another title"), s); err == nil {
		t.Fatal("a second task under the same name should be rejected")
	}
}

func TestDecideTaskAddIsCapped(t *testing.T) {
	s := existingThread()
	for i := 0; i < maxTasks; i++ {
		s.Tasks = append(s.Tasks, TaskItem{Name: string(rune('a' + i%26)), Title: "t"})
	}
	if _, err := Decide(taskAdd("one-more", "over the cap"), s); err == nil {
		t.Fatal("a task past the cap should be rejected")
	}
}

func TestDecideTaskStatusNeedsAKnownTaskAndStatus(t *testing.T) {
	s := existingThread()
	s.Tasks = []TaskItem{{Name: "api", Title: "Design the API", Status: contracts.TaskTodo}}
	if _, err := Decide(taskStatus("missing", contracts.TaskDone), s); err == nil {
		t.Fatal("updating an unknown task should be rejected")
	}
	if _, err := Decide(taskStatus("api", "blocked"), s); err == nil {
		t.Fatal("an unknown status should be rejected")
	}
	evs, err := Decide(taskStatus("api", contracts.TaskDoing), s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(evs) != 1 || evs[0].(contracts.TaskStatusChanged).Status != contracts.TaskDoing {
		t.Fatalf("want one TaskStatusChanged{doing}, got %#v", evs)
	}
}

func TestProjectTasksFollowsAddAndStatus(t *testing.T) {
	s := existingThread()
	apply := func(p contracts.EventPayload) {
		t.Helper()
		if err := Project(s, contracts.Event{ThreadID: "t1", Payload: p}); err != nil {
			t.Fatal(err)
		}
	}
	apply(contracts.TaskAdded{Name: "api", Title: "Design the API"})
	apply(contracts.TaskAdded{Name: "db", Title: "Schema"})
	apply(contracts.TaskStatusChanged{Name: "db", Status: contracts.TaskDone})
	apply(contracts.TaskStatusChanged{Name: "gone", Status: contracts.TaskDone}) // no-op on replay

	want := []TaskItem{
		{Name: "api", Title: "Design the API", Status: contracts.TaskTodo},
		{Name: "db", Title: "Schema", Status: contracts.TaskDone},
	}
	if len(s.Tasks) != len(want) {
		t.Fatalf("tasks = %+v, want %+v", s.Tasks, want)
	}
	for i := range want {
		if s.Tasks[i] != want[i] {
			t.Fatalf("tasks[%d] = %+v, want %+v", i, s.Tasks[i], want[i])
		}
	}
}
