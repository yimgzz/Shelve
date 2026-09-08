package sshengine

// Keyboard-interactive (bastion) auth, plan P009. One
// vault:kbdint-prompt modal per server INFO_REQUEST round. The challenge
// closure is per-connection / per-dial (fresh round counter and prefill
// flag on every build), so a key-passphrase re-run or a retry re-attaches
// a clean closure and the counter resets naturally.

import (
	"context"
	"errors"
	"time"

	"golang.org/x/crypto/ssh"
)

// maxKbdintRounds bounds the question rounds a single keyboard-interactive
// exchange may take before the handshake is aborted (defense against a
// misbehaving or hostile bastion looping prompts). The completion round
// (zero questions) does not count.
const maxKbdintRounds = 10

// bastionChallenge returns the ssh.KeyboardInteractiveChallenge for one
// bastion hop. Per round it:
//   - answers the completion round (zero questions) with nil,nil so
//     x/crypto sends the final empty response and no modal is shown;
//   - emits vault:kbdint-prompt (name/instruction/questions/echo plus a
//     first-round-only prefill) and blocks on the prompt slot until the
//     round is submitted, cancelled, the per-round PromptTimeout elapses,
//     or the connection ctx is cancelled;
//   - aborts the handshake once maxKbdintRounds question rounds are hit.
//
// The closure captures connID (the prompt-slot key) and prefill (the hop's
// stored password when password-auth; the §8 prefill-only exception).
func (m *Manager) bastionChallenge(ctx context.Context, connID, prefill string) ssh.KeyboardInteractiveChallenge {
	round := 0
	return func(name, instruction string, questions []string, echos []bool) ([]string, error) {
		if len(questions) == 0 {
			// Completion round: confirm so x/crypto writes the empty
			// final response (the server then grants success).
			return nil, nil
		}
		if round >= maxKbdintRounds {
			return nil, errors.New("too many keyboard-interactive rounds")
		}
		round++
		// Prefill (the bastion hop's stored password, §8 exception) is
		// offered for every password-style round (plan P009 §2.4); the
		// user overwrites it when a later round needs a different answer.
		ch, slot := m.beginKbdintPrompt(connID, name, instruction, questions, echos, prefill)
		// The Submit/Cancel paths remove the slot via resolvePrompt; the
		// timeout/cancel paths remove it here.
		defer m.discardPrompt(connID, slot)
		var timer *time.Timer
		var timerC <-chan time.Time
		if t := m.promptTimeout(); t > 0 {
			timer = time.NewTimer(t)
			timerC = timer.C
		}
		defer func() {
			if timer != nil {
				timer.Stop()
			}
		}()
		select {
		case res := <-ch:
			if res.aborted {
				return nil, errors.New("keyboard-interactive prompt cancelled")
			}
			return res.answers, nil
		case <-timerC:
			return nil, errors.New("keyboard-interactive prompt timed out")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
