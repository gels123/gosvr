package topology

import (
	"gnet/lib/conf"
	"gnet/lib/core"
	"gnet/lib/encoding/gob"
	"gnet/lib/logsimple"
	"gnet/lib/network/tcp"
)

type Node struct {
	Agent core.sid
	Name  string
}

type master struct {
	*core.ServiceBase
	nodesMap      map[uint64]Node //nodeid : Node struct
	globalNameMap map[string]core.sid
	tcpServer     *tcp.Server
	isNeedExit    bool
}

func StartMaster(ip, port string) {
	m := &master{ServiceBase: core.NewSkeleton(0)}
	m.nodesMap = make(map[uint64]Node)
	m.globalNameMap = make(map[string]core.sid)
	core.NewService(&core.ModuleParam{
		name: ".router",
		M:    m,
		L:    0,
	})

	if !conf.CoreIsStandalone {
		m.tcpServer = tcp.NewServer(ip, port, m.Id)
		m.tcpServer.SetAcceptWhiteIPList(conf.SlaveWhiteIPList)
		m.tcpServer.Listen()
	}
}

func (m *master) OnNormalMSG(msg *core.Message) {
	//cmd such as (registerName, getIdByName, syncName, forward ...)
	cmd := msg.Cmd
	data := msg.Data
	switch cmd {
	case core.Cmd_Forward:
		msg := data[0].(*core.Message)
		m.forwardM(msg, nil)
	case core.Cmd_RegisterName:
		id := data[0].(uint64)
		name := data[1].(string)
		m.onRegisterName(core.sid(id), name)
	case core.Cmd_GetIdByName:
		name := data[0].(string)
		rid := data[1].(uint)
		id, ok := m.globalNameMap[name]
		core.DispatchGetIdByNameRet(id, ok, name, rid)
	case core.Cmd_Exit:
		m.closeAll()
	case core.Cmd_Exit_Node:
		nodeName := data[0].(string)
		m.closeNode(nodeName)
	case core.Cmd_RefreshSlaveWhiteIPList:
		ips := data[0].([]string)
		m.tcpServer.SetAcceptWhiteIPList(ips)
	default:
		logsimple.Info("Unknown command for master: %v", cmd)
	}
}

func (m *master) onRegisterNode(src core.sid, nodeName string) {
	//generate node id
	nodeId := core.GenerateNodeId()
	logsimple.Info("register node: nodeId: %v, nodeName: %v", nodeId, nodeName)
	m.nodesMap[nodeId] = Node{
		Agent: src,
		Name:  nodeName,
	}
	msg := core.NewMessage(core.INVALID_SRC_ID, core.INVALID_SRC_ID, core.MSG_TYPE_NORMAL, core.MSG_ENC_TYPE_NO, 0, core.Cmd_RegisterNodeRet, nodeId)
	sendData := gob.Pack(msg)
	m.RawSend(src, core.MSG_TYPE_NORMAL, tcp.AGENT_CMD_SEND, sendData)
}

func (m *master) onRegisterName(serviceId core.sid, serviceName string) {
	m.globalNameMap[serviceName] = serviceId
	m.distributeM(core.Cmd_NameAdd, core.NodeInfo{serviceName, serviceId})
}

func (m *master) onGetIdByName(src core.sid, name string, rId uint) {
	id, ok := m.globalNameMap[name]
	msg := core.NewMessage(core.INVALID_SRC_ID, core.INVALID_SRC_ID, core.MSG_TYPE_NORMAL, core.MSG_ENC_TYPE_NO, 0, core.Cmd_GetIdByNameRet, id, ok, name, rId)
	sendData := gob.Pack(msg)
	m.RawSend(src, core.MSG_TYPE_NORMAL, tcp.AGENT_CMD_SEND, sendData)
}

func (m *master) OnSocketMSG(msg *core.Message) {
	//src is slave's agent's serviceid
	src := msg.From
	//cmd is socket status
	cmd := msg.Cmd
	//data[0] is a gob encode with message
	data := msg.Data
	//it's first encode value is cmd such as (registerNode, regeisterName, getIdByName, forward...)
	if cmd == tcp.AGENT_DATA {
		sdata, err := gob.Unpack(data[0].([]byte))
		if err != nil {
			m.SendClose(src, false)
			return
		}
		slaveMSG := sdata.([]interface{})[0].(*core.Message)
		scmd := slaveMSG.Cmd
		array := slaveMSG.Data
		switch scmd {
		case core.Cmd_RegisterNode:
			nodeName := array[0].(string)
			m.onRegisterNode(src, nodeName)
		case core.Cmd_RegisterName:
			serviceId := array[0].(uint64)
			serviceName := array[1].(string)
			m.onRegisterName(core.sid(serviceId), serviceName)
		case core.Cmd_GetIdByName:
			name := array[0].(string)
			rId := array[1].(uint)
			m.onGetIdByName(src, name, rId)
		case core.Cmd_Forward:
			//find correct agent and send msg to that node.
			forwardMsg := array[0].(*core.Message)
			m.forwardM(forwardMsg, data[0].([]byte))
		case core.Cmd_Exit:
			m.closeAll()
		case core.Cmd_Exit_Node:
			nodeName := array[0].(string)
			m.closeNode(nodeName)
		case core.Cmd_RefreshSlaveWhiteIPList:
			ips := array[0].([]string)
			m.tcpServer.SetAcceptWhiteIPList(ips)
		}
	} else if cmd == tcp.AGENT_CLOSED {
		//on agent disconnected
		//delet node from nodesMap
		var nodeId uint64 = 0
		hasFind := false
		for id, v := range m.nodesMap {
			if v.Agent == src {
				hasFind = true
				nodeId = id
			}
		}
		if !hasFind {
			return
		}
		delete(m.nodesMap, nodeId)
		core.CollectNodeId(nodeId)

		//notify other services delete name's id on agent which is disconnected.
		deletedNames := []interface{}{}
		for name, id := range m.globalNameMap {
			nid := core.ParseNodeId(id)
			if nid == nodeId {
				logsimple.Warn("service is delete: name: %v id: %v", name, id)
				deletedNames = append(deletedNames, core.NodeInfo{name, id})
				delete(m.globalNameMap, name)
			}
		}
		m.distributeM(core.Cmd_NameDeleted, deletedNames...)

		if len(m.nodesMap) == 0 && m.isNeedExit {
			core.SendCloseToAll()
		}
	}
}

func (m *master) distributeM(cmd core.CmdType, data ...interface{}) {
	for _, node := range m.nodesMap {
		msg := &core.Message{}
		msg.Cmd = core.Cmd_Distribute
		msg.Data = append(msg.Data, cmd)
		msg.Data = append(msg.Data, data...)
		sendData := gob.Pack(msg)
		m.RawSend(node.Agent, core.MSG_TYPE_NORMAL, tcp.AGENT_CMD_SEND, sendData)
	}
	core.DistributeMSG(m.Id, cmd, data...)
}

func (m *master) closeNode(nodeName string) {
	for _, node := range m.nodesMap {
		if node.Name == nodeName {
			msg := &core.Message{}
			msg.Cmd = core.Cmd_Exit
			sendData := gob.Pack(msg)
			m.RawSend(node.Agent, core.MSG_TYPE_NORMAL, tcp.AGENT_CMD_SEND, sendData)
		}
	}
}

func (m *master) closeAll() {
	m.isNeedExit = true
	for _, node := range m.nodesMap {
		msg := &core.Message{}
		msg.Cmd = core.Cmd_Exit
		sendData := gob.Pack(msg)
		m.RawSend(node.Agent, core.MSG_TYPE_NORMAL, tcp.AGENT_CMD_SEND, sendData)
	}
	if len(m.nodesMap) == 0 {
		core.SendCloseToAll()
	}
}

func (m *master) forwardM(msg *core.Message, data []byte) {
	nodeId := core.ParseNodeId(core.sid(msg.Dst))
	isLcoal := core.CheckIsLocalServiceId(core.sid(msg.Dst))
	//log.Debug("master forwardM is send to master: %v, nodeid: %d", isLcoal, nodeId)
	if isLcoal {
		core.ForwardLocal(msg)
		return
	}
	node, ok := m.nodesMap[nodeId]
	if !ok {
		logsimple.Debug("node:%v is disconnected.", nodeId)
		return
	}
	//if has no encode data, encode it first.
	if data == nil {
		ret := &core.Message{
			Cmd: core.Cmd_Forward,
		}
		ret.Data = append(ret.Data, msg)
		data = gob.Pack(ret)
	}
	m.RawSend(node.Agent, core.MSG_TYPE_NORMAL, tcp.AGENT_CMD_SEND, data)
}

func (m *master) OnDestroy() {
	if m.tcpServer != nil {
		m.tcpServer.Close()
	}
	for _, v := range m.nodesMap {
		m.SendClose(v.Agent, false)
	}
}
