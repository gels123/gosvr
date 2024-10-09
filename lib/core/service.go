// 服务基类, 供外部服务继承, 创建新服务流程: NewService()->SetSelf()->Start()
package core

import (
	"errors"
	"gnet/lib/conf"
	"gnet/lib/encoding/gob"
	"gnet/lib/logzap"
	"gnet/lib/timer"
	"gnet/lib/utils"
	"reflect"
	"sync"
	"time"
)

// 服务接口(IService)
type IService interface {
	// 初始化
	OnInit()
	// 销毁
	OnDestroy()
	//OnMainLoop is called ever main loop, the delta time is specific by GetDuration()
	OnMainLoop(dt int) //dt is the duration time(unit Millisecond)
	//OnNormalMSG is called when received msg from Send() or RawSend() with MSG_TYPE_NORMAL
	OnNormalMSG(msg *Message)
	//OnSocketMSG is called when received msg from Send() or RawSend() with MSG_TYPE_SOCKET
	OnSocketMSG(msg *Message)
	//OnRequestMSG is called when received msg from Request()
	OnRequestMSG(msg *Message)
	//OnCallMSG is called when received msg from Call()
	OnCallMSG(msg *Message)
	//OnDistributeMSG is called when received msg from Send() or RawSend() with MSG_TYPE_DISTRIBUTE
	OnDistributeMSG(msg *Message)
	//OnCloseNotify is called when received msg from SendClose() with false param.
	OnCloseNotify()
}

type ServiceOptions struct {
	name  string // 服务名称
	msgSz int    // 消息缓冲大小
	tick  int    // 间隔(ms)
}

// 服务基类, 实现服务接口(IService)
type ServiceBase struct {
	self              IService                      // self
	sid               SvcId                         // 服务ID
	name              string                        // 服务名称
	msgChan           chan *Message                 // 消息通道
	msgSz             int                           // 消息缓冲大小
	reqId             uint64                        //
	requestMap        map[uint64]requestCB          //
	requestMutex      sync.Mutex                    //
	callId            uint64                        //
	callChanMap       map[uint64]chan []interface{} //
	callMutex         sync.Mutex                    //
	tick              int                           // 间隔(毫秒ms)
	ts                *timer.TimerSchedule          // 计时器
	normalDispatcher  *CallHelper                   //
	requestDispatcher *CallHelper                   //
	callDispatcher    *CallHelper                   //
}

type requestCB struct {
	respond reflect.Value
	//timeout reflect.Value
}

// 创建一个新服务,
func NewService(opt ServiceOptions) SvcId {
	if len(opt.name) == 0 {
		panic("new service error: name invalid or repeat")
	}
	s := &ServiceBase{
		self: nil,
		name: opt.name,
	}
	if opt.msgSz <= 1024 {
		opt.msgSz = 1024
	}
	s.msgSz = opt.msgSz
	s.msgChan = make(chan *Message, s.msgSz)
	s.reqId = 0
	s.requestMap = make(map[uint64]requestCB)
	s.callChanMap = make(map[uint64]chan []interface{})
	sid := registService(s)
	return sid
}

// 设置子类/派生类
func (s *ServiceBase) SetSelf(self IService) {
	s.self = self
}

// 获取服务ID
func (s *ServiceBase) GetId() SvcId {
	return s.sid
}

// 设置服务ID
func (s *ServiceBase) setId(id SvcId) {
	s.sid = id
}

// 获取服务名称
func (s *ServiceBase) GetName() string {
	return s.name
}

// 设置服务名称
func (s *ServiceBase) setName(name string) {
	s.name = name
}

// 启动服务
// @tick  计时器间隔, 大于0时启动计时器
func (s *ServiceBase) Start(tick int) {
	if tick < 0 {
		tick = 0
	}
	s.tick = tick
	if s.tick > 0 && s.ts == nil {
		s.ts = timer.NewTimerSchedule()
		s.ts.SetTick(s.tick)
		s.ts.Start()
	}
	utils.SafeGo(s.loop)
}

// 循环分发消息
func (s *ServiceBase) loop() {
	// 初始化
	s.OnInit()
	// 循环分发消息
	for {
		if !s.loopSelect() {
			break
		}
	}
	s.OnDestroy()
}

// 分发消息
func (s *ServiceBase) loopSelect() bool {
	defer func() {
		if err := recover(); err != nil {
			logzap.Errorw("service error", "service", s.GetName(), "stack", utils.GetStack())
		}
	}()
	select {
	case msg, ok := <-s.msgChan:
		if !ok {
			return false
		}
		ok = s.dispatchMSG(msg)
		if !ok {
			return false
		}
	}
	return true
}

// 初始化
func (s *ServiceBase) OnInit() {

}

// 销毁
func (s *ServiceBase) OnDestroy() {
	unregistService(s)
	if s.msgChan != nil {
		close(s.msgChan)
		s.msgChan = nil
	}
}

// 压入消息
func (s *ServiceBase) pushMsg(msg *Message) {
	select {
	case s.msgChan <- msg:
	default:
		if s.msgChan == nil {
			logzap.Warnw("service pushMsg error: chan is nil", "service", s.GetName(), "stack", utils.GetStack())
		} else {
			logzap.Warnw("service pushMsg error: chan is full", "service", s.GetName(), "stack", utils.GetStack())
		}
	}
}

// 分发消息
func (s *ServiceBase) dispatchMSG(msg *Message) bool {
	if msg.EncType == MSG_ENC_TYPE_GOB {
		t, err := gob.Unpack(msg.Data[0].([]byte))
		if err != nil {
			panic(err)
		}
		msg.Data = t.([]interface{})
	}
	switch msg.Type {
	case MSG_TYPE_NORMAL:
		s.OnNormalMSG(msg)
	case MSG_TYPE_CLOSE:
		if msg.Data[0].(bool) {
			return false
		}
		s.OnCloseNotify()
	case MSG_TYPE_SOCKET:
		s.OnSocketMSG(msg)
	case MSG_TYPE_REQUEST:
		s.dispatchRequest(msg)
	case MSG_TYPE_RESPOND:
		s.dispatchRespond(msg)
	case MSG_TYPE_CALL:
		s.dispatchCall(msg)
	case MSG_TYPE_DISTRIBUTE:
		s.OnDistributeMSG(msg)
	case MSG_TYPE_TIMEOUT:
		s.dispatchTimeout(msg)
	}
	return true
}

// respndCb is a function like: func(isok bool, ...interface{})  the first param must be a bool
func (s *ServiceBase) request(dst SvcId, encType EncType, timeout int, respondCb interface{}, cmd CmdType, data ...interface{}) {
	s.requestMutex.Lock()
	id := s.reqId
	s.reqId++
	cbp := requestCB{reflect.ValueOf(respondCb)}
	s.requestMap[id] = cbp
	s.requestMutex.Unlock()
	utils.PanicWhen(cbp.respond.Kind() != reflect.Func, "respond cb must function.")

	lowLevelSend(s.GetId(), dst, MSG_TYPE_REQUEST, encType, id, cmd, data...)

	if timeout > 0 {
		time.AfterFunc(time.Duration(timeout)*time.Millisecond, func() {
			s.requestMutex.Lock()
			_, ok := s.requestMap[id]
			s.requestMutex.Unlock()
			if ok {
				lowLevelSend(INVALID_SRC_ID, s.getId(), MSG_TYPE_TIMEOUT, MSG_ENC_TYPE_NO, id, Cmd_None)
			}
		})
	}
}

func (s *ServiceBase) dispatchTimeout(m *Message) {
	rid := m.Id
	cbp, ok := s.getDeleteRequestCb(rid)
	if !ok {
		return
	}
	cb := cbp.respond
	var param []reflect.Value
	param = append(param, reflect.ValueOf(true))
	plen := cb.Type().NumIn()
	for i := 1; i < plen; i++ {
		param = append(param, reflect.New(cb.Type().In(i)).Elem())
	}
	cb.Call(param)
}

func (s *ServiceBase) dispatchRequest(msg *Message) {
	s.OnRequestMSG(msg)
}

func (s *ServiceBase) respond(dst SvcId, encType EncType, rid uint64, data ...interface{}) {
	lowLevelSend(s.getId(), dst, MSG_TYPE_RESPOND, encType, rid, Cmd_None, data...)
}

// return request callback by request sid
func (s *ServiceBase) getDeleteRequestCb(id uint64) (requestCB, bool) {
	s.requestMutex.Lock()
	cb, ok := s.requestMap[id]
	delete(s.requestMap, id)
	s.requestMutex.Unlock()
	return cb, ok
}

func (s *ServiceBase) dispatchRespond(m *Message) {
	var rid uint64
	var data []interface{}
	rid = m.Id
	data = m.Data

	cbp, ok := s.getDeleteRequestCb(rid)
	if !ok {
		return
	}
	cb := cbp.respond
	n := len(data)
	param := make([]reflect.Value, n+1)
	param[0] = reflect.ValueOf(false)
	HelperFunctionToUseReflectCall(cb, param, 1, data)
	cb.Call(param)
}

func (s *ServiceBase) call(dst SvcId, encType EncType, cmd CmdType, data ...interface{}) ([]interface{}, error) {
	utils.PanicWhen(dst == s.getId(), "dst must equal to s's sid")
	s.callMutex.Lock()
	id := s.callId
	s.callId++
	s.callMutex.Unlock()

	//ch has one buffer, make ret service not block on it.
	ch := make(chan []interface{}, 1)
	s.callMutex.Lock()
	s.callChanMap[id] = ch
	s.callMutex.Unlock()
	if err := lowLevelSend(s.getId(), dst, MSG_TYPE_CALL, encType, id, cmd, data...); err != nil {
		return nil, err
	}
	if conf.CallTimeOut > 0 {
		time.AfterFunc(time.Duration(conf.CallTimeOut)*time.Millisecond, func() {
			s.dispatchRet(id, errors.New("call time out"))
		})
	}
	ret := <-ch
	s.callMutex.Lock()
	delete(s.callChanMap, id)
	s.callMutex.Unlock()

	close(ch)
	if err, ok := ret[0].(error); ok {
		return ret[1:], err
	}
	return ret, nil
}

func (s *ServiceBase) callWithTimeout(dst SvcId, encType EncType, timeout int, cmd CmdType, data ...interface{}) ([]interface{}, error) {
	utils.PanicWhen(dst == s.getId(), "dst must equal to s's sid")
	s.callMutex.Lock()
	id := s.callId
	s.callId++
	s.callMutex.Unlock()

	//ch has one buffer, make ret service not block on it.
	ch := make(chan []interface{}, 1)
	s.callMutex.Lock()
	s.callChanMap[id] = ch
	s.callMutex.Unlock()
	if err := lowLevelSend(s.getId(), dst, MSG_TYPE_CALL, encType, id, cmd, data...); err != nil {
		return nil, err
	}
	if timeout > 0 {
		time.AfterFunc(time.Duration(timeout)*time.Millisecond, func() {
			s.dispatchRet(id, errors.New("call time out"))
		})
	}
	ret := <-ch
	s.callMutex.Lock()
	delete(s.callChanMap, id)
	s.callMutex.Unlock()

	close(ch)
	if err, ok := ret[0].(error); ok {
		return ret[1:], err
	}
	return ret, nil
}

func (s *ServiceBase) dispatchCall(msg *Message) {
	s.OnCallMSG(msg)
}

func (s *ServiceBase) ret(dst SvcId, encType EncType, cid uint64, data ...interface{}) {
	var dstService *ServiceBase
	dstService, err := findServiceById(dst)
	if err != nil {
		lowLevelSend(s.getId(), dst, MSG_TYPE_RET, encType, cid, Cmd_None, data...)
		return
	}
	dstService.dispatchRet(cid, data...)
}

func (s *ServiceBase) dispatchRet(cid uint64, data ...interface{}) {
	s.callMutex.Lock()
	ch, ok := s.callChanMap[cid]
	s.callMutex.Unlock()

	if ok {
		select {
		case ch <- data:
		default:
			utils.PanicWhen(true, "dispatchRet failed on ch.")
		}
	}
}

func (s *ServiceBase) schedule(interval, repeat int, cb timer.TimerCallback) *timer.Timer {
	utils.PanicWhen(s.tick <= 0, "loopDuraton must greater than zero.")
	return s.ts.Schedule(interval, repeat, cb)
}

// xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx

func (s *ServiceBase) OnModuleStartup(sid SvcId, serviceName string) {
	s.normalDispatcher = NewCallHelper(serviceName + ":normalDispatcher")
	s.requestDispatcher = NewCallHelper(serviceName + ":requestDispatcher")
	s.callDispatcher = NewCallHelper(serviceName + ":callDispatcher")
}

// Send消息
func (s *ServiceBase) Send(addr SvcId, msgType MsgType, encType EncType, cmd CmdType, data ...interface{}) {
	lowLevelSend(s.getId(), addr, msgType, encType, 0, cmd, data...)
}

// RawSend not encode variables, be careful use
// variables that passed by reference may be changed by others
func (s *ServiceBase) RawSend(dst SvcId, msgType MsgType, cmd CmdType, data ...interface{}) {
	sendNoEnc(s.getId(), dst, msgType, 0, cmd, data...)
}

// if isForce is false, then it will just notify the sevice it need to close
// then service can do choose close immediate or close after self clean.
// if isForce is true, then it close immediate
func (s *ServiceBase) SendClose(dst SvcId, isForce bool) {
	sendNoEnc(s.getId(), dst, MSG_TYPE_CLOSE, 0, Cmd_None, isForce)
}

// Request send a request msg to dst, and Start timeout function if timeout > 0, millisecond
// after receiver call Respond, the responseCb will be called
func (s *ServiceBase) Request(dst SvcId, encType EncType, timeout int, responseCb interface{}, cmd CmdType, data ...interface{}) {
	s.request(dst, encType, timeout, responseCb, cmd, data...)
}

// Respond used to respond request msg
func (s *ServiceBase) Respond(dst SvcId, encType EncType, rid uint64, data ...interface{}) {
	s.respond(dst, encType, rid, data...)
}

// Call send a call msg to dst, and Start a timeout function with the conf.CallTimeOut
// after receiver call Ret, it will return
func (s *ServiceBase) Call(dst SvcId, encType EncType, cmd CmdType, data ...interface{}) ([]interface{}, error) {
	return s.call(dst, encType, cmd, data...)
}

// CallWithTimeout send a call msg to dst, and Start a timeout function with the timeout millisecond
// after receiver call Ret, it will return
func (s *ServiceBase) CallWithTimeout(dst SvcId, encType EncType, timeout int, cmd CmdType, data ...interface{}) ([]interface{}, error) {
	return s.callWithTimeout(dst, encType, timeout, cmd, data...)
}

// Schedule schedule a time with given parameter.
func (s *ServiceBase) Schedule(interval, repeat int, cb timer.TimerCallback) *timer.Timer {
	if s == nil {
		panic("Schedule must call after OnInit is called(not contain OnInit)")
	}
	return s.schedule(interval, repeat, cb)
}

// Ret used to ret call msg
func (s *ServiceBase) Ret(dst SvcId, encType EncType, cid uint64, data ...interface{}) {
	s.ret(dst, encType, cid, data...)
}

func (s *ServiceBase) OnMainLoop(dt int) {
}

// 分发普通消息
func (s *ServiceBase) OnNormalMSG(msg *Message) {
	s.normalDispatcher.Call(msg.Cmd, msg.Src, msg.Data...)
}

func (s *ServiceBase) OnSocketMSG(msg *Message) {
}
func (s *ServiceBase) OnRequestMSG(msg *Message) {
	isAutoReply := s.requestDispatcher.getIsAutoReply(msg.Cmd)
	if isAutoReply { //if auto reply is set, auto respond when user's callback is return.
		ret := s.requestDispatcher.Call(msg.Cmd, msg.Src, msg.Data...)
		s.Respond(msg.Src, msg.EncType, msg.Id, ret...)
	} else { //pass a closure to the user's callback, when to call depends on the user.
		s.requestDispatcher.CallWithReplyFunc(msg.Cmd, msg.Src, func(ret ...interface{}) {
			s.Respond(msg.Src, msg.EncType, msg.Id, ret...)
		}, msg.Data...)
	}
}
func (s *ServiceBase) OnCallMSG(msg *Message) {
	isAutoReply := s.callDispatcher.getIsAutoReply(msg.Cmd)
	if isAutoReply {
		ret := s.callDispatcher.Call(msg.Cmd, msg.Src, msg.Data...)
		s.Ret(msg.Src, msg.EncType, msg.Id, ret...)
	} else {
		s.callDispatcher.CallWithReplyFunc(msg.Cmd, msg.Src, func(ret ...interface{}) {
			s.Ret(msg.Src, msg.EncType, msg.Id, ret...)
		}, msg.Data...)
	}
}

func (s *ServiceBase) findCallerByType(msgType MsgType) *CallHelper {
	var caller *CallHelper
	switch msgType {
	case MSG_TYPE_NORMAL:
		caller = s.normalDispatcher
	case MSG_TYPE_REQUEST:
		caller = s.requestDispatcher
	case MSG_TYPE_CALL:
		caller = s.callDispatcher
	default:
		panic("not support msgType")
	}
	return caller
}

// function's first parameter must sid
// isAutoReply: is auto reply when msgType is request or call.
func (s *ServiceBase) RegisterHandlerFunc(msgType MsgType, cmd CmdType, fun interface{}, isAutoReply bool) {
	caller := s.findCallerByType(msgType)
	caller.AddFunc(cmd, fun)
	caller.setIsAutoReply(cmd, isAutoReply)
}

// method's first parameter must sid
// isAutoReply: is auto reply when msgType is request or call.
func (s *ServiceBase) RegisterHandlerMethod(msgType MsgType, cmd CmdType, v interface{}, methodName string, isAutoReply bool) {
	caller := s.findCallerByType(msgType)
	caller.AddMethod(cmd, v, methodName)
	caller.setIsAutoReply(cmd, isAutoReply)
}

func (s *ServiceBase) OnDistributeMSG(msg *Message) {
}

func (s *ServiceBase) OnCloseNotify() {
	s.SendClose(s.getId(), true)
}
