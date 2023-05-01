package main

import (
	"raft"
	"time"
)

// check whether the request is outdated
func (kv *KVNode) IsOutdated(client int64, msgIdx int64, update bool) (rst bool) {
	kv.mu.Lock()
	defer kv.mu.Unlock()

	rst = false
	latestIdx, ok := kv.OperationRecord[client]
	// if exists
	if ok {
		rst = latestIdx >= msgIdx // msgidx should be larger than latestidx
	}
	if update && !rst {
		// not outdated and update signal is given
		kv.OperationRecord[client] = msgIdx
	}

	return
}

// put/append op
func (kv *KVNode) PutAppendOp(req *PutAppendArgs) {
	if req.Op == "Put" {
		kv.KeyValueStorage[req.Key] = req.Value
	} else if req.Op == "Append" {
		value, ok := kv.KeyValueStorage[req.Key]
		if !ok {
			value = ""
		}
		value += req.Value
		kv.KeyValueStorage[req.Key] = value
	}
}

// read op
func (kv *KVNode) GetOp(args *GetArgs) (value string) {
	value, ok := kv.KeyValueStorage[args.Key]
	if !ok {
		value = ""
	}
	return
}

// Get interface for client
func (kv *KVNode) Get(req *GetArgs, rep *GetReply) error {
	ok, val := kv.DoOperation(-1, -1, *req)
	rep.Success = ok
	if ok {
		rep.Value = val.(string)
	}
	return nil
}

// Put/Append interface for client
func (kv *KVNode) PutAppend(req *PutAppendArgs, rep *PutAppendReply) error {
	ok, _ := kv.DoOperation(req.Me, req.MsgId, *req)
	rep.Success = ok
	return nil
}

// using raft layer to do key-value management operations
// msgIdx == -1 if it is a read op
func (kv *KVNode) DoOperation(client int64, msgIdx int64, req interface{}) (bool, interface{}) {
	// check whether the put/append op is outdated
	if msgIdx > 0 && kv.IsOutdated(client, msgIdx, false) {
		return true, nil
	}

	// initialize the operation
	curOp := Operation{
		Req: req,
		Ch:  make(chan interface{}, 1),
	}

	// call raft layer interface
	successPushedIntoApplyChan := kv.RaftNode.HandleKVOperation(curOp)
	if successPushedIntoApplyChan == false {
		// not leader or timeout
		return false, nil
	}

	// wait the operation be finally implemented by kv-server (now it is already applied by raft and push into apply channel)
	select {
	case ret := <-curOp.Ch:
		// applied by the kv system
		return true, ret
	case <-time.After(time.Millisecond * 5000):
		return false, nil
	}

}

// implement operations that are applied by raft layer
func (kv *KVNode) ApplyOperation(applyMsg raft.ApplyMsg) {
	if !applyMsg.CommandValid {
		// invalid or snapshot
		// TODO: need to add support for snapshot later
		return
	}

	// update the apply index
	kv.AppliedLogIndex = applyMsg.CommandIndex
	// get the operation (type conversion)
	curOp := applyMsg.Command.(Operation)
	// response to curOp.Ch
	var response interface{}

	// handle key value operation

	// if it is putappend op
	if command, ok := curOp.Req.(PutAppendArgs); ok {
		if !kv.IsOutdated(command.Me, command.MsgId, true) {
			kv.PutAppendOp(&command)
		}
		response = true
	} else {
		// it is get op
		command := curOp.Req.(GetArgs)
		response = kv.GetOp(&command)
	}

	// announce the client, end the DoOperation
	select {
	case curOp.Ch <- response:
	default:
	}
}
