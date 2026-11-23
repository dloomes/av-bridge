package portalapi

// Teams Rooms on Windows read directly by a Collector over WinRM (protocol
// mtr_windows). They are ordinary Collector devices of type teams_room with
// platform windows; Poly Lens rooms (source poly_lens) keep their own rules.

const (
	deviceTypeTeamsRoom  = "teams_room"
	protocolMTRWindows   = "mtr_windows"
	msgTeamsRoomProtocol = "a Collector Teams Room uses protocol mtr_windows, and mtr_windows is for Teams Rooms only"
)

// validateCollectorTeamsRoom returns a 400 message for a Collector device
// create that breaks the Teams Room rules, or "". A Teams Room with no
// platform is taken as windows (the only platform a Collector reads).
func validateCollectorTeamsRoom(req *createDeviceReq) string {
	if req.Type == deviceTypeTeamsRoom && req.Platform == "" {
		req.Platform = "windows"
	}
	if req.ExternalID != "" {
		return "external_id applies to Poly Lens devices only"
	}
	teams := req.Type == deviceTypeTeamsRoom
	if teams != (req.Protocol == protocolMTRWindows) {
		return msgTeamsRoomProtocol
	}
	switch {
	case teams && req.Platform != "windows":
		return "platform must be windows for a Teams Room read by a Collector"
	case !teams && req.Platform != "":
		return "platform applies to Teams Rooms only"
	}
	return ""
}
