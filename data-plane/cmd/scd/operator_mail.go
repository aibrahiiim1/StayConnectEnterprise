package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	netmail "net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/onegate/onegate/data-plane/internal/mail"
	"github.com/onegate/onegate/data-plane/internal/notifyloader"
)

var operatorResetCode = regexp.MustCompile(`^[0-9]{8}$`)

func (s *server) operatorMailKind() string {
	s.notify.mu.RLock()
	defer s.notify.mu.RUnlock()
	return s.notify.mailKind
}

func operatorMailReady(kind string) bool { return kind == notifyloader.KindSMTP }

func (s *server) operatorMailState(w http.ResponseWriter, r *http.Request) {
	kind := s.operatorMailKind()
	writeJSON(w, http.StatusOK, map[string]any{"kind": kind, "ready": operatorMailReady(kind)})
}

type operatorMailRequest struct {
	To       string `json:"to"`
	Template string `json:"template"`
	Code     string `json:"code"`
	Minutes  int    `json:"minutes"`
}

func operatorMailMessage(in operatorMailRequest) (mail.Message, string) {
	addr, err := netmail.ParseAddress(strings.TrimSpace(in.To))
	if err != nil || addr.Name != "" {
		return mail.Message{}, "a recipient address is required"
	}
	switch in.Template {
	case "reset_code":
		if !operatorResetCode.MatchString(in.Code) {
			return mail.Message{}, "the code must be 8 digits"
		}
		if in.Minutes < 5 || in.Minutes > 60 {
			return mail.Message{}, "the code lifetime must be between 5 and 60 minutes"
		}
		return mail.Message{
			To:      addr.Address,
			Subject: "Your OneGate Admin Console password reset code",
			Text: "Someone asked to reset the OneGate Admin Console password for this email address.\n\n" +
				"Your code is: " + in.Code + "\n\n" +
				fmt.Sprintf("It works once, for %d minutes. Enter it on the page where you asked for it.\n\n", in.Minutes) +
				"If you did not ask for this, ignore this email. Your password has not changed. " +
				"If it keeps happening, tell your site administrator.\n",
		}, ""
	case "password_changed":
		return mail.Message{
			To:      addr.Address,
			Subject: "Your OneGate Admin Console password was changed",
			Text: "The OneGate Admin Console password for this email address was just changed with a reset code sent to this address. " +
				"Every session of this account was signed out.\n\n" +
				"If you did not do this, tell your site administrator now. They can reset your password and check Activity for what happened.\n",
		}, ""
	}
	return mail.Message{}, "unknown template"
}

func (s *server) operatorMailSend(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in operatorMailRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	msg, problem := operatorMailMessage(in)
	if problem != "" {
		httpErr(w, http.StatusBadRequest, problem)
		return
	}
	if !operatorMailReady(s.operatorMailKind()) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "mail_not_ready"})
		return
	}
	mailer, _, _ := s.senders()
	if mailer == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "mail_not_ready"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := mailer.Send(ctx, msg); err != nil {
		slog.Warn("operator email not delivered", "template", in.Template, "to", mail.MaskAddress(msg.To), "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "send_failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"sent": true})
}
