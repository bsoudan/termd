package main

import (
	"fmt"
	"log/slog"

	"termd/config"
	"termd/frontend/protocol"
)

// ── Request types sent to the server event loop ──────────────────────────────

type addClientReq struct {
	client *Client
	resp   chan struct{}
}

type removeClientReq struct {
	clientID uint32
}

type spawnRegionReq struct {
	region      *Region
	sessionName string
	resp        chan struct{}
}

type destroyRegionReq struct {
	regionID string
	resp     chan destroyResult
}

type destroyResult struct {
	region      *Region
	subscribers []*Client
	found       bool
}

type findRegionReq struct {
	regionID string
	resp     chan *Region
}

type getClientsReq struct {
	resp chan []*Client
}

type killRegionReq struct {
	regionID string
	resp     chan *Region
}

type killClientReq struct {
	clientID uint32
	resp     chan *Client
}

type getSubscribersReq struct {
	regionID string
	resp     chan []*Client
}

type statusCounts struct {
	numClients  int
	numRegions  int
	numSessions int
}

type getStatusReq struct {
	resp chan statusCounts
}

type lookupProgramReq struct {
	name string
	resp chan *config.ProgramConfig
}

type sessionConnectResult struct {
	exists         bool
	regionInfos    []protocol.RegionInfo
	programConfigs []config.ProgramConfig
}

type sessionConnectReq struct {
	name string
	resp chan sessionConnectResult
}

type listProgramsReq struct {
	resp chan []protocol.ProgramInfo
}

type addProgramReq struct {
	prog config.ProgramConfig
	resp chan error
}

type removeProgramReq struct {
	name string
	resp chan error
}

type getRegionInfosReq struct {
	session string
	resp    chan []protocol.RegionInfo
}

type getClientInfosReq struct {
	resp chan []protocol.ClientInfoData
}

type getSessionInfosReq struct {
	resp chan []protocol.SessionInfo
}

type subscribeReq struct {
	clientID uint32
	regionID string
	resp     chan *Region // nil if region not found
}

type unsubscribeReq struct {
	clientID uint32
}

type setClientSessionReq struct {
	clientID    uint32
	sessionName string
}

type getClientSessionReq struct {
	clientID uint32
	resp     chan string
}

type shutdownResult struct {
	clients []*Client
	regions []*Region
}

// ── Event loop ───────────────────────────────────────────────────────────────

func (s *Server) eventLoop() {
	regions := s.initRegions
	clients := s.initClients
	sessions := s.initSessions
	programs := s.initPrograms

	// Clear init references so only the event loop owns these maps.
	s.initRegions = nil
	s.initClients = nil
	s.initSessions = nil
	s.initPrograms = nil

	subscriptions := make(map[uint32]string)          // clientID → regionID
	regionSubs := make(map[string]map[uint32]struct{}) // regionID → set of clientIDs
	clientSessions := make(map[uint32]string)          // clientID → sessionName

	for {
		select {
		case req := <-s.requests:
			switch r := req.(type) {

			case addClientReq:
				clients[r.client.id] = r.client
				r.resp <- struct{}{}

			case removeClientReq:
				if rid, ok := subscriptions[r.clientID]; ok {
					delete(subscriptions, r.clientID)
					if s := regionSubs[rid]; s != nil {
						delete(s, r.clientID)
						if len(s) == 0 {
							delete(regionSubs, rid)
						}
					}
				}
				delete(clients, r.clientID)
				delete(clientSessions, r.clientID)
				slog.Debug("client disconnected", "id", r.clientID)

			case spawnRegionReq:
				regions[r.region.id] = r.region
				sess := sessions[r.sessionName]
				if sess == nil {
					sess = NewSession(r.sessionName)
					sessions[r.sessionName] = sess
				}
				sess.regions[r.region.id] = r.region
				r.region.session = r.sessionName
				r.resp <- struct{}{}

			case destroyRegionReq:
				region, ok := regions[r.regionID]
				if !ok {
					r.resp <- destroyResult{found: false}
					break
				}
				delete(regions, r.regionID)
				if sess := sessions[region.session]; sess != nil {
					delete(sess.regions, r.regionID)
					if len(sess.regions) == 0 {
						delete(sessions, region.session)
						slog.Info("removed empty session", "session", region.session)
					}
				}
				var subscribers []*Client
				if subs := regionSubs[r.regionID]; subs != nil {
					for clientID := range subs {
						delete(subscriptions, clientID)
						if c, ok := clients[clientID]; ok {
							subscribers = append(subscribers, c)
						}
					}
					delete(regionSubs, r.regionID)
				}
				r.resp <- destroyResult{region: region, subscribers: subscribers, found: true}

			case findRegionReq:
				r.resp <- regions[r.regionID]

			case getClientsReq:
				list := make([]*Client, 0, len(clients))
				for _, c := range clients {
					list = append(list, c)
				}
				r.resp <- list

			case killRegionReq:
				r.resp <- regions[r.regionID]

			case killClientReq:
				r.resp <- clients[r.clientID]

			case getSubscribersReq:
				var subs []*Client
				for clientID := range regionSubs[r.regionID] {
					if c, ok := clients[clientID]; ok {
						subs = append(subs, c)
					}
				}
				r.resp <- subs

			case getStatusReq:
				r.resp <- statusCounts{
					numClients:  len(clients),
					numRegions:  len(regions),
					numSessions: len(sessions),
				}

			case lookupProgramReq:
				if p, ok := programs[r.name]; ok {
					r.resp <- &p
				} else {
					r.resp <- nil
				}

			case sessionConnectReq:
				name := r.name
				if name == "" {
					name = s.sessionsCfg.DefaultName
				}
				if sess, exists := sessions[name]; exists {
					infos := make([]protocol.RegionInfo, 0, len(sess.regions))
					for _, reg := range sess.regions {
						infos = append(infos, protocol.RegionInfo{
							RegionID: reg.id,
							Name:     reg.name,
							Cmd:      reg.cmd,
							Pid:      reg.pid,
							Session:  sess.name,
						})
					}
					r.resp <- sessionConnectResult{exists: true, regionInfos: infos}
					break
				}
				programNames := s.sessionsCfg.DefaultPrograms
				if len(programNames) == 0 {
					if _, ok := programs["default"]; ok {
						programNames = []string{"default"}
					} else {
						for pname := range programs {
							programNames = []string{pname}
							break
						}
					}
				}
				var configs []config.ProgramConfig
				for _, pname := range programNames {
					if p, ok := programs[pname]; ok {
						configs = append(configs, p)
					}
				}
				r.resp <- sessionConnectResult{exists: false, programConfigs: configs}

			case listProgramsReq:
				infos := make([]protocol.ProgramInfo, 0, len(programs))
				for _, p := range programs {
					infos = append(infos, protocol.ProgramInfo{Name: p.Name, Cmd: p.Cmd})
				}
				r.resp <- infos

			case addProgramReq:
				if _, exists := programs[r.prog.Name]; exists {
					r.resp <- fmt.Errorf("program %q already exists", r.prog.Name)
				} else {
					programs[r.prog.Name] = r.prog
					r.resp <- nil
				}

			case removeProgramReq:
				if _, exists := programs[r.name]; !exists {
					r.resp <- fmt.Errorf("program %q not found", r.name)
				} else {
					delete(programs, r.name)
					r.resp <- nil
				}

			case getRegionInfosReq:
				if r.session != "" {
					sess := sessions[r.session]
					if sess == nil {
						r.resp <- nil
						break
					}
					infos := make([]protocol.RegionInfo, 0, len(sess.regions))
					for _, reg := range sess.regions {
						infos = append(infos, protocol.RegionInfo{
							RegionID: reg.id,
							Name:     reg.name,
							Cmd:      reg.cmd,
							Pid:      reg.pid,
							Session:  sess.name,
						})
					}
					r.resp <- infos
					break
				}
				infos := make([]protocol.RegionInfo, 0, len(regions))
				for _, reg := range regions {
					infos = append(infos, protocol.RegionInfo{
						RegionID: reg.id,
						Name:     reg.name,
						Cmd:      reg.cmd,
						Pid:      reg.pid,
						Session:  reg.session,
					})
				}
				r.resp <- infos

			case getClientInfosReq:
				infos := make([]protocol.ClientInfoData, 0, len(clients))
				for _, c := range clients {
					infos = append(infos, protocol.ClientInfoData{
						ClientID:           c.id,
						Hostname:           c.GetHostname(),
						Username:           c.GetUsername(),
						Pid:                c.GetPid(),
						Process:            c.GetProcess(),
						Session:            clientSessions[c.id],
						SubscribedRegionID: subscriptions[c.id],
					})
				}
				r.resp <- infos

			case subscribeReq:
				region, exists := regions[r.regionID]
				if !exists {
					r.resp <- nil
					break
				}
				// Remove from previous subscription if any.
				if prev, ok := subscriptions[r.clientID]; ok && prev != r.regionID {
					if s := regionSubs[prev]; s != nil {
						delete(s, r.clientID)
						if len(s) == 0 {
							delete(regionSubs, prev)
						}
					}
				}
				subscriptions[r.clientID] = r.regionID
				if regionSubs[r.regionID] == nil {
					regionSubs[r.regionID] = make(map[uint32]struct{})
				}
				regionSubs[r.regionID][r.clientID] = struct{}{}
				r.resp <- region

			case unsubscribeReq:
				if rid, ok := subscriptions[r.clientID]; ok {
					delete(subscriptions, r.clientID)
					if s := regionSubs[rid]; s != nil {
						delete(s, r.clientID)
						if len(s) == 0 {
							delete(regionSubs, rid)
						}
					}
				}

			case setClientSessionReq:
				clientSessions[r.clientID] = r.sessionName

			case getClientSessionReq:
				r.resp <- clientSessions[r.clientID]

			case getSessionInfosReq:
				infos := make([]protocol.SessionInfo, 0, len(sessions))
				for _, sess := range sessions {
					infos = append(infos, protocol.SessionInfo{
						Name:       sess.name,
						NumRegions: len(sess.regions),
					})
				}
				r.resp <- infos

			// --- Live upgrade support ---

			case disconnectAllReq:
				for _, c := range clients {
					c.Close()
				}
				clients = make(map[uint32]*Client)
				subscriptions = make(map[uint32]string)
				regionSubs = make(map[string]map[uint32]struct{})
				clientSessions = make(map[uint32]string)
				r.resp <- struct{}{}

			case upgradeReq:
				r.resp <- upgradeResult{
					regions:  regions,
					sessions: sessions,
					programs: programs,
				}
				// Pause: wait for resume (rollback) or done (successful upgrade).
				select {
				case <-s.requests:
					// resumeUpgradeReq — put state back and continue.
					// (The actual type is checked below; here we just drain.)
				case <-s.done:
					s.shutdownResp <- shutdownResult{}
					return
				}

			case resumeUpgradeReq:
				// No-op; handled by the pause select in upgradeReq above.

			case restoreRegionReq:
				regions[r.region.id] = r.region
				sess, ok := sessions[r.session]
				if !ok {
					sess = &Session{name: r.session, regions: make(map[string]*Region)}
					sessions[r.session] = sess
				}
				sess.regions[r.region.id] = r.region
				r.region.session = r.session
				go s.watchRegion(r.region)
				r.resp <- struct{}{}
			}

		case <-s.done:
			clientList := make([]*Client, 0, len(clients))
			for _, c := range clients {
				clientList = append(clientList, c)
			}
			regionList := make([]*Region, 0, len(regions))
			for _, r := range regions {
				regionList = append(regionList, r)
			}
			s.shutdownResp <- shutdownResult{clients: clientList, regions: regionList}
			return
		}
	}
}
