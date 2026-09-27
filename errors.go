package steam

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// EResult is Steam's generic result code (x-eresult header, "strError ... (N)" suffixes, error bodies).
type EResult int

const (
	EResultInvalid                 EResult = 0
	EResultOK                      EResult = 1
	EResultFail                    EResult = 2
	EResultNoConnection            EResult = 3
	EResultInvalidPassword         EResult = 5
	EResultLoggedInElsewhere       EResult = 6
	EResultInvalidProtocolVer      EResult = 7
	EResultInvalidParam            EResult = 8
	EResultFileNotFound            EResult = 9
	EResultBusy                    EResult = 10
	EResultInvalidState            EResult = 11
	EResultInvalidName             EResult = 12
	EResultInvalidEmail            EResult = 13
	EResultDuplicateName           EResult = 14
	EResultAccessDenied            EResult = 15
	EResultTimeout                 EResult = 16
	EResultBanned                  EResult = 17
	EResultAccountNotFound         EResult = 18
	EResultInvalidSteamID          EResult = 19
	EResultServiceUnavailable      EResult = 20
	EResultNotLoggedOn             EResult = 21
	EResultPending                 EResult = 22
	EResultEncryptionFailure       EResult = 23
	EResultInsufficientPrivilege   EResult = 24
	EResultLimitExceeded           EResult = 25
	EResultRevoked                 EResult = 26
	EResultExpired                 EResult = 27
	EResultAlreadyRedeemed         EResult = 28
	EResultDuplicateRequest        EResult = 29
	EResultAlreadyOwned            EResult = 30
	EResultIPNotFound              EResult = 31
	EResultPersistFailed           EResult = 32
	EResultLockingFailed           EResult = 33
	EResultLogonSessionReplaced    EResult = 34
	EResultConnectFailed           EResult = 35
	EResultHandshakeFailed         EResult = 36
	EResultIOFailure               EResult = 37
	EResultRemoteDisconnect        EResult = 38
	EResultBlocked                 EResult = 40
	EResultIgnored                 EResult = 41
	EResultNoMatch                 EResult = 42
	EResultAccountDisabled         EResult = 43
	EResultServiceReadOnly         EResult = 44
	EResultTryAnotherCM            EResult = 48
	EResultCannotUseOldPassword    EResult = 51
	EResultInvalidLoginAuthCode    EResult = 65
	EResultAccountLogonDenied      EResult = 63
	EResultAccountLockedDown       EResult = 73
	EResultRateLimitExceeded       EResult = 84
	EResultTwoFactorCodeMismatch   EResult = 88
	EResultItemDeleted             EResult = 94
	EResultTooManyPending          EResult = 102
	EResultNoMobileDevice          EResult = 103
	EResultAccountLimitExceeded    EResult = 116
	EResultAccountActivityLimitExc EResult = 117
	EResultTimeNotSynced           EResult = 120
)

var eresultNames = map[EResult]string{
	0: "Invalid", 1: "OK", 2: "Fail", 3: "NoConnection", 5: "InvalidPassword", 6: "LoggedInElsewhere",
	7: "InvalidProtocolVer", 8: "InvalidParam", 9: "FileNotFound", 10: "Busy", 11: "InvalidState",
	12: "InvalidName", 13: "InvalidEmail", 14: "DuplicateName", 15: "AccessDenied", 16: "Timeout",
	17: "Banned", 18: "AccountNotFound", 19: "InvalidSteamID", 20: "ServiceUnavailable", 21: "NotLoggedOn",
	22: "Pending", 23: "EncryptionFailure", 24: "InsufficientPrivilege", 25: "LimitExceeded", 26: "Revoked",
	27: "Expired", 28: "AlreadyRedeemed", 29: "DuplicateRequest", 30: "AlreadyOwned", 31: "IPNotFound",
	32: "PersistFailed", 33: "LockingFailed", 34: "LogonSessionReplaced", 35: "ConnectFailed",
	36: "HandshakeFailed", 37: "IOFailure", 38: "RemoteDisconnect", 40: "Blocked", 41: "Ignored",
	42: "NoMatch", 43: "AccountDisabled", 44: "ServiceReadOnly", 48: "TryAnotherCM", 51: "CannotUseOldPassword",
	63: "AccountLogonDenied", 65: "InvalidLoginAuthCode", 73: "AccountLockedDown", 84: "RateLimitExceeded",
	88: "TwoFactorCodeMismatch", 94: "ItemDeleted", 102: "TooManyPending", 103: "NoMobileDevice",
	116: "AccountLimitExceeded", 117: "AccountActivityLimitExceeded", 120: "TimeNotSynced",
}

func (r EResult) String() string {
	if s, ok := eresultNames[r]; ok {
		return s
	}
	return "EResult(" + strconv.Itoa(int(r)) + ")"
}

// Retryable reports codes that usually go away on their own (busy / rate limited / backend hiccups).
func (r EResult) Retryable() bool {
	switch r {
	case EResultFail, EResultBusy, EResultTimeout, EResultServiceUnavailable, EResultTryAnotherCM,
		EResultRateLimitExceeded, EResultNoConnection, EResultIOFailure, EResultRemoteDisconnect,
		EResultPersistFailed, EResultLockingFailed, EResultConnectFailed:
		return true
	}
	return false
}

// Error causes (SteamError.Cause), mirroring node-steam-tradeoffer-manager's error.cause values.
const (
	CauseTradeBan              = "TradeBan"
	CauseNewDevice             = "NewDevice"
	CauseTargetCannotTrade     = "TargetCannotTrade"
	CauseOfferLimitExceeded    = "OfferLimitExceeded"
	CauseItemServerUnavailable = "ItemServerUnavailable"
	CauseNotLoggedIn           = "NotLoggedIn"
	CausePrivateInventory      = "PrivateInventory"
	CauseRateLimited           = "RateLimited"
	CauseInvalidTradeURL       = "InvalidTradeURL"
)

// SteamError is the structured error returned by the context-aware helpers of this package.
// Use errors.As to inspect it, or the IsXxx helpers below.
type SteamError struct {
	Op         string        // e.g. "SendOffer", "GetTradeStatus"
	HTTPStatus int           // 0 when the transport failed or the status was 200
	EResult    EResult       // 0 when unknown
	Message    string        // Steam's message (strError / error / body excerpt)
	Cause      string        // one of the Cause* constants, "" when unknown
	RetryAfter time.Duration // from a Retry-After header (429), 0 when absent
}

func (e *SteamError) Error() string {
	var b strings.Builder
	b.WriteString("steam")
	if e.Op != "" {
		b.WriteString(" ")
		b.WriteString(e.Op)
	}
	b.WriteString(":")
	if e.HTTPStatus != 0 {
		b.WriteString(" HTTP ")
		b.WriteString(strconv.Itoa(e.HTTPStatus))
	}
	if e.EResult != 0 && e.EResult != EResultOK {
		b.WriteString(" eresult=")
		b.WriteString(e.EResult.String())
	}
	if e.Cause != "" {
		b.WriteString(" cause=")
		b.WriteString(e.Cause)
	}
	if e.Message != "" {
		b.WriteString(" ")
		b.WriteString(e.Message)
	}
	return b.String()
}

// Temporary reports whether retrying later may succeed.
func (e *SteamError) Temporary() bool {
	if e.HTTPStatus == http.StatusTooManyRequests || e.HTTPStatus >= 500 {
		return true
	}
	if e.Cause == CauseItemServerUnavailable || e.Cause == CauseRateLimited {
		return true
	}
	return e.EResult.Retryable()
}

var (
	// ErrInventoryPrivate: GET /inventory answered 403 with a "null" body (private profile or inventory).
	ErrInventoryPrivate = errors.New("steam: inventory is private")
	// ErrNotLoggedIn: the web session expired (HTTP 401, or a login page instead of JSON).
	ErrNotLoggedIn = errors.New("steam: not logged in")
	// ErrOfferNotFound: GetTradeOffer returned an empty response for the id.
	ErrOfferNotFound = errors.New("steam: trade offer not found")
	// ErrTradeNotFound: GetTradeStatus did not return the trade (yet).
	ErrTradeNotFound = errors.New("steam: trade not found")
	// ErrInvalidTradeURL: the string is not a https://steamcommunity.com/tradeoffer/new/?partner=..&token=.. link.
	ErrInvalidTradeURL = errors.New("steam: invalid trade offer URL")
	// ErrConfirmationNotFound: no pending mobile confirmation exists for the object (offer id).
	ErrConfirmationNotFound = errors.New("steam: confirmation not found")
)

func (e *SteamError) Is(target error) bool {
	switch target {
	case ErrNotLoggedIn:
		return e.HTTPStatus == http.StatusUnauthorized || e.Cause == CauseNotLoggedIn
	case ErrInventoryPrivate:
		return e.Cause == CausePrivateInventory
	}
	return false
}

// IsRateLimited reports HTTP 429 / RateLimitExceeded errors.
func IsRateLimited(err error) bool {
	var se *SteamError
	if errors.As(err, &se) {
		return se.HTTPStatus == http.StatusTooManyRequests || se.EResult == EResultRateLimitExceeded || se.Cause == CauseRateLimited
	}
	return false
}

// IsTemporary reports errors worth retrying later (rate limits, 5xx, busy, network timeouts).
func IsTemporary(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var se *SteamError
	if errors.As(err, &se) {
		return se.Temporary()
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// RetryAfterOf returns the server-requested delay of a rate-limited error (0 when unknown).
func RetryAfterOf(err error) time.Duration {
	var se *SteamError
	if errors.As(err, &se) {
		return se.RetryAfter
	}
	return 0
}

var strErrorEResultExp = regexp.MustCompile(`\((\d+)\)\s*$`)

// newStrError maps a trade-endpoint "strError" (e.g. "There was an error sending your trade offer. (26)")
// to a SteamError with the EResult and a cause, like tradeoffer-manager's Helpers.makeAnError.
func newStrError(op string, status int, strError string) *SteamError {
	e := &SteamError{Op: op, HTTPStatus: status, Message: strings.TrimSpace(strError)}
	if status == http.StatusOK {
		e.HTTPStatus = 0
	}
	if m := strErrorEResultExp.FindStringSubmatch(strError); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			e.EResult = EResult(n)
		}
	}
	switch {
	case strings.Contains(strError, "because they have a trade ban"):
		e.Cause = CauseTradeBan
	case strings.Contains(strError, "You have logged in from a new device"):
		e.Cause = CauseNewDevice
	case strings.Contains(strError, "is not available to trade. More information will be shown to"):
		e.Cause = CauseTargetCannotTrade
	case strings.Contains(strError, "sent too many trade offers"):
		e.Cause = CauseOfferLimitExceeded
		e.EResult = EResultLimitExceeded
	case strings.Contains(strError, "unable to contact the game's item server"):
		e.Cause = CauseItemServerUnavailable
		e.EResult = EResultServiceUnavailable
	}
	return e
}

// httpError builds a SteamError for a non-200 response (body excerpt kept short, never includes cookies).
func httpError(op string, resp *http.Response, body []byte) *SteamError {
	e := &SteamError{Op: op, HTTPStatus: resp.StatusCode}
	if xe := resp.Header.Get("X-Eresult"); xe != "" {
		if n, err := strconv.Atoi(xe); err == nil {
			e.EResult = EResult(n)
		}
	}
	if resp.StatusCode == http.StatusUnauthorized {
		e.Cause = CauseNotLoggedIn
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		e.Cause = CauseRateLimited
		e.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	}
	msg := strings.TrimSpace(string(body))
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	if msg != "" && msg != "null" && !strings.HasPrefix(msg, "<") {
		e.Message = msg
	}
	return e
}

func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func wrapf(op string, err error) error {
	if err == nil {
		return nil
	}
	var se *SteamError
	if errors.As(err, &se) {
		return err
	}
	return fmt.Errorf("steam %s: %w", op, err)
}
