package bridge

import "shelve/internal/app"

// RegisterAll wires the composition root's services into the server's
// dispatch registry. Both the production entry point (cmd/shelve-backend) and
// the golden surface test use this single path, so the registered surface
// cannot drift from the tested contract.
func (s *Server) RegisterAll(a *app.App) {
	s.Register("AppService", a.AppService())
	s.Register("VaultService", a.VaultService())
	s.Register("SessionService", a.SessionService())
	s.Register("CredentialService", a.CredentialService())
	s.Register("JumpHostService", a.JumpHostService())
	s.Register("TerminalService", a.TerminalService())
	s.Register("SftpService", a.SftpService())
	s.Register("MonitorService", a.MonitorService())
	s.Register("TransferService", a.TransferService())
}
