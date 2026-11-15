package execution

// procState e a maquina explicita do ciclo do processo interativo.
//
// Diagrama (texto):
//
//	running --Wait retornou--> reaped --markDone--> done
//	removido = sair do mapa (sem estado; List filtra done, Len conta tudo)
//
// running: Wait ainda nao retornou (reaped aberto, done aberto, exitCode nil).
// reaped:  Wait retornou (reaped fechado, done aberto, exitCode ainda nil).
// done:    resultado publicado (reaped fechado, done fechado, exitCode fixado).
// "removido" nao e estado: e delete do mapa (Remove/expiracao).
type procState int

const (
	procRunning procState = iota
	procReaped
	procDone
)

func (s procState) String() string {
	switch s {
	case procRunning:
		return "running"
	case procReaped:
		return "reaped"
	case procDone:
		return "done"
	default:
		return "unknown"
	}
}

// procEvent sao os unicos eventos que movem a maquina.
type procEvent int

const (
	// procEvWaitReturned: Wait() do exec retornou (Start goroutine, apos close antigo).
	procEvWaitReturned procEvent = iota
	// procEvPublished: resultado pronto para publicar (markDone).
	procEvPublished
)

func (e procEvent) String() string {
	switch e {
	case procEvWaitReturned:
		return "waitReturned"
	case procEvPublished:
		return "published"
	default:
		return "unknown"
	}
}

// procNext e a UNICA tabela de transicoes legais (estado x evento).
func procNext(cur procState, ev procEvent) (procState, bool) {
	switch cur {
	case procRunning:
		if ev == procEvWaitReturned {
			return procReaped, true
		}
	case procReaped:
		if ev == procEvPublished {
			return procDone, true
		}
	case procDone:
		// Terminal: nenhuma saida.
	}
	return cur, false
}

// procDonePayload carrega os gemeos publicados juntos no reaped->done.
type procDonePayload struct {
	waitErr   error
	exitCode  int
	report    AdapterReport
	reportErr error
	changes   []Change
}

// transition e o UNICO ponto que escreve o estado (state + gemeos) e o unico
// que fecha reaped/done. Nunca da panic: transicao ilegal ou payload nil
// recusa sem escrever nada e retorna false.
func (process *managedProcess) transition(ev procEvent, payload *procDonePayload) bool {
	process.mu.Lock()
	defer process.mu.Unlock()
	next, ok := procNext(process.state, ev)
	if !ok {
		return false
	}
	switch ev {
	case procEvWaitReturned:
		process.state = next
		if process.reaped != nil {
			select {
			case <-process.reaped:
				// Ja fechado (literal manual legado); nao fecha de novo.
			default:
				close(process.reaped)
			}
		}
		return true
	case procEvPublished:
		if payload == nil {
			return false
		}
		process.waitErr = payload.waitErr
		value := payload.exitCode
		process.exitCode = &value
		process.resultReport = payload.report
		process.reportErr = payload.reportErr
		process.changes = append([]Change(nil), payload.changes...)
		process.state = next
		if process.done != nil {
			process.doneOnce.Do(func() { close(process.done) })
		}
		return true
	default:
		return false
	}
}
