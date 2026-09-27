package steam

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

type ConfirmationResponse struct {
	Success       bool            `json:"success"`
	Confirmations []*Confirmation `json:"conf"`
}

type Confirmation struct {
	ID           string `json:"id"`
	Type         uint8  `json:"type"`
	Creator      string `json:"creator_id"`
	Nonce        string `json:"nonce"`
	CreationTime uint64 `json:"creation"`
	// Fields of the current mobileconf/getlist JSON.
	CreationTimeUnix uint64 `json:"creation_time"`
	TypeName         string `json:"type_name"`
	Headline         string `json:"headline"`

	// TypeName string      `json:"type_name"`
	// Cancel   string      `json:"cancel"`
	// Accept   string      `json:"accept"`
	// Icon     string      `json:"icon"`
	// Multi    bool        `json:"multi"`
	// Headline string      `json:"headline"`
	// Summary  []string    `json:"summary"`
	// Warn     interface{} `json:"warn"`
}

var (
	//ErrConfirmationsUnknownError = errors.New("unknown error occurred finding confirmation")
	ErrCannotFindConfirmations   = errors.New("unable to find confirmation")
	ErrCannotFindDescriptions    = errors.New("unable to find confirmation descriptions")
	ErrConfirmationsDescMismatch = errors.New("cannot match confirmation with their respective descriptions")
	ErrWGTokenExpired            = errors.New("WGToken expired")
)

func (session *Session) execConfirmationRequest(request, key, tag string, current int64, values map[string]string) (*http.Response, error) {
	params := url.Values{
		"p":   {session.deviceID},
		"a":   {session.oauth.SteamID.ToString()},
		"k":   {key},
		"t":   {strconv.FormatInt(current, 10)},
		"m":   {"android"},
		"tag": {tag},
	}

	for k, v := range values {
		params.Add(k, v)
	}

	return session.client.Get("https://steamcommunity.com/mobileconf/" + request + params.Encode())
}

func (session *Session) GetConfirmations(identitySecret string, current int64) ([]*Confirmation, error) {
	key, err := GenerateConfirmationCode(identitySecret, "conf", current)
	if err != nil {
		return nil, err
	}

	resp, err := session.execConfirmationRequest("getlist?", key, "conf", current, nil)
	if resp != nil {
		defer resp.Body.Close()
	}

	if err != nil {
		return nil, err
	}

	var confirmationResponse ConfirmationResponse

	b, _ := io.ReadAll(resp.Body)

	//d := json.NewDecoder(resp.Body)
	if err = json.Unmarshal(b, &confirmationResponse); err != nil {
		return nil, err
	}
	if !confirmationResponse.Success {
		var extra struct {
			NeedAuth bool   `json:"needauth"`
			Message  string `json:"message"`
		}
		_ = json.Unmarshal(b, &extra)
		se := &SteamError{Op: "GetConfirmations", Message: extra.Message}
		if extra.NeedAuth {
			se.Cause = CauseNotLoggedIn
		}
		if se.Message == "" {
			se.Message = "unsuccessful response"
		}
		return nil, se
	}

	return confirmationResponse.Confirmations, nil
}

func (session *Session) AnswerConfirmation(confirmation *Confirmation, identitySecret, answer string, current int64) error {
	key, err := GenerateConfirmationCode(identitySecret, answer, current)
	if err != nil {
		return err
	}

	op := map[string]string{
		"op":  answer,
		"cid": confirmation.ID,
		"ck":  confirmation.Nonce,
	}

	resp, err := session.execConfirmationRequest("ajaxop?", key, answer, current, op)
	if resp != nil {
		defer resp.Body.Close()
	}

	if err != nil {
		return err
	}

	type Response struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}

	var response Response
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return err
	}

	if !response.Success {
		return errors.New(response.Message)
	}

	return nil
}

func (confirmation *Confirmation) Answer(session *Session, key, answer string, current int64) error {
	return session.AnswerConfirmation(confirmation, key, answer, current)
}

// ConfirmationTypeTrade is the mobileconf "type" of trade offer confirmations.
const ConfirmationTypeTrade = 2

// AcceptConfirmationForObject finds the pending mobile confirmation created for objectID (a trade offer id)
// and accepts it, like steamcommunity's acceptConfirmationForObject. current is the Steam-aligned unix time.
// Returns ErrConfirmationNotFound when no such confirmation is pending.
func (session *Session) AcceptConfirmationForObject(identitySecret string, objectID uint64, current int64) error {
	confs, err := session.GetConfirmations(identitySecret, current)
	if err != nil {
		return err
	}
	want := strconv.FormatUint(objectID, 10)
	for _, c := range confs {
		if c != nil && c.Creator == want {
			// tag/op "allow" = accept (same as node-steamcommunity's respondToConfirmation).
			return session.AnswerConfirmation(c, identitySecret, "allow", current)
		}
	}
	return ErrConfirmationNotFound
}
