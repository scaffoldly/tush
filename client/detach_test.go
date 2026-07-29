package client

import (
	"bytes"
	"testing"
)

// TestSplitDetach covers the sequence recognition on its own, including the
// cases that only appear when a keystroke lands on a read boundary.
func TestSplitDetach(t *testing.T) {
	cases := []struct {
		name   string
		typed  []byte
		send   []byte
		detach bool
		held   []byte
	}{{
		name:  "ordinary input passes through",
		typed: []byte("ls -l\n"),
		send:  []byte("ls -l\n"),
	}, {
		name:   "the sequence detaches",
		typed:  []byte{detachPrefix, detachSuffix},
		detach: true,
	}, {
		name:   "input before the sequence still reaches the host",
		typed:  append([]byte("hi"), detachPrefix, detachSuffix),
		send:   []byte("hi"),
		detach: true,
	}, {
		name:  "a lone prefix is held until the next key explains it",
		typed: []byte("hi\x10"),
		send:  []byte("hi"),
		held:  []byte{detachPrefix},
	}, {
		name:  "a prefix followed by anything else is ordinary input",
		typed: []byte{detachPrefix, 'a'},
		send:  []byte{detachPrefix, 'a'},
	}, {
		name:  "the suffix alone means nothing",
		typed: []byte{detachSuffix},
		send:  []byte{detachSuffix},
	}, {
		name:  "a repeated prefix holds only the last",
		typed: []byte{detachPrefix, detachPrefix},
		send:  []byte{detachPrefix},
		held:  []byte{detachPrefix},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			send, detach, held := splitDetach(tc.typed)
			if !bytes.Equal(send, tc.send) {
				t.Errorf("send = %q, want %q", send, tc.send)
			}
			if detach != tc.detach {
				t.Errorf("detach = %v, want %v", detach, tc.detach)
			}
			if !bytes.Equal(held, tc.held) {
				t.Errorf("held = %q, want %q", held, tc.held)
			}
		})
	}
}

// TestSplitDetachAcrossReads checks the sequence still counts when the two keys
// arrive in separate reads, which is what happens when a user types them.
func TestSplitDetachAcrossReads(t *testing.T) {
	send, detach, held := splitDetach([]byte{detachPrefix})
	if len(send) != 0 || detach || !bytes.Equal(held, []byte{detachPrefix}) {
		t.Fatalf("first read: send=%q detach=%v held=%q", send, detach, held)
	}

	send, detach, held = splitDetach(append(held, detachSuffix))
	if len(send) != 0 || !detach || held != nil {
		t.Errorf("second read: send=%q detach=%v held=%q", send, detach, held)
	}
}
