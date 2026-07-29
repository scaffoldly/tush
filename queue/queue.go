package queue

import "sync"

type Queue[T any] struct {
	items   []T
	itemsMu sync.Mutex
}

func New[T any](elements []T) *Queue[T] {
	copied := make([]T, len(elements))
	copy(copied, elements)
	return &Queue[T]{items: copied}
}

func (q *Queue[T]) Shift() T {
	q.itemsMu.Lock()
	defer q.itemsMu.Unlock()
	var zero T
	if len(q.items) == 0 {
		return zero
	}
	item := q.items[0]
	q.items = q.items[1:]
	return item
}
