package task

type Task struct {
	Id     int    `json:"id"`
	Status string `json:"status"`
	Name   string `json:"name"`
}
