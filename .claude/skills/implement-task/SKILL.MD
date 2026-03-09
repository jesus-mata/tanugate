Based on the file @tasks.json implement only and only the tasks which have the status open, and the ones that does not have dependencies in the depends_on property. When you find a tasks select one and set the status to "in progress" and read the plan file specified in the plan property and start implement and work on it.
The user may send the task number when calling the skill, work on the specified task if exist, example `/implement-task 3` means work on task number 3.
Once you complete with the task set the status of the task to "complete" and run this valdations:

- gofmt -s -w .
- go test ./...
- go vet ./...

Also after completing the task, append to @progress.txt:

- Task completed
- Key decisions made and reasoning
- Files changed
- Any blockers or notes for next iteration

Keep entries concise. Sacrifice grammar for the sake of concision. This file helps future iterations skip exploration.
