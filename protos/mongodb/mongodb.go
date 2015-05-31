package mongodb

import (
	"packetbeat/common"
	"packetbeat/config"
	"packetbeat/logp"
	"packetbeat/procs"
	"packetbeat/protos"
	"packetbeat/protos/tcp"
	"time"
)

type Mongodb struct {
	// config
	Send_request  bool
	Send_response bool

	transactionsMap map[common.HashableTcpTuple]*MongodbTransaction

	results chan common.MapStr
}

func (mongodb *Mongodb) InitDefaults() {
	mongodb.Send_request = false
	mongodb.Send_response = false
}

func (mongodb *Mongodb) setFromConfig() error {
	if config.ConfigMeta.IsDefined("protocols", "mongodb", "send_request") {
		mongodb.Send_request = config.ConfigSingleton.Protocols["mongodb"].Send_request
	}
	if config.ConfigMeta.IsDefined("protocols", "mongodb", "send_response") {
		mongodb.Send_response = config.ConfigSingleton.Protocols["mongodb"].Send_response
	}
	return nil
}

func (mongodb *Mongodb) Init(test_mode bool, results chan common.MapStr) error {
	logp.Debug("mongodb", "Init a MongoDB protocol parser")

	mongodb.InitDefaults()
	if !test_mode {
		mongodb.setFromConfig()
	}

	mongodb.transactionsMap = make(map[common.HashableTcpTuple]*MongodbTransaction, TransactionsHashSize)
	mongodb.results = results

	return nil
}

func (mongodb *Mongodb) Parse(pkt *protos.Packet, tcptuple *common.TcpTuple, dir uint8,
	private protos.ProtocolData) protos.ProtocolData {

	logp.Debug("mongodb", "Parse method triggered")

	defer logp.Recover("ParseMongodb exception")

	// Either fetch or initialize current data struct for this parser
	priv := mongodbPrivateData{}
	if private != nil {
		var ok bool
		priv, ok = private.(mongodbPrivateData)
		if !ok {
			priv = mongodbPrivateData{}
		}
	}

	if priv.Data[dir] == nil {
		priv.Data[dir] = &MongodbStream{
			tcptuple: tcptuple,
			data:     pkt.Payload,
			message:  &MongodbMessage{Ts: pkt.Ts},
		}
	} else {
		// concatenate bytes
		priv.Data[dir].data = append(priv.Data[dir].data, pkt.Payload...)
		if len(priv.Data[dir].data) > tcp.TCP_MAX_DATA_IN_STREAM {
			logp.Debug("mongodb", "Stream data too large, dropping TCP stream")
			priv.Data[dir] = nil
			return priv
		}
	}

	stream := priv.Data[dir]
	for len(stream.data) > 0 {
		if stream.message == nil {
			stream.message = &MongodbMessage{Ts: pkt.Ts}
		}

		ok, complete := mongodbMessageParser(priv.Data[dir])

		if !ok {
			// drop this tcp stream. Will retry parsing with the next
			// segment in it
			priv.Data[dir] = nil
			logp.Debug("mongodb", "Ignore Mongodb message. Drop tcp stream. Try parsing with the next segment")
			return priv
		}

		if complete {

			logp.Debug("mongodb", "MongoDB message complete")

			// all ok, go to next level
			mongodb.handleMongodb(stream.message, tcptuple, dir)

			// and reset message
			stream.PrepareForNewMessage()
		} else {
			// wait for more data
			logp.Debug("mongodb", "MongoDB wait for more data before parsing message")
			break
		}
	}

	return priv
}

func (mongodb *Mongodb) handleMongodb(m *MongodbMessage, tcptuple *common.TcpTuple,
	dir uint8) {

	m.TcpTuple = *tcptuple
	m.Direction = dir
	m.CmdlineTuple = procs.ProcWatcher.FindProcessesTuple(tcptuple.IpPort())

	if m.IsResponse {
		logp.Debug("mongodb", "MongoDB response message")
		mongodb.receivedMongodbResponse(m)
	} else {
		logp.Debug("mongodb", "MongoDB request message")
		mongodb.receivedMongodbRequest(m)
	}
}

func (mongodb *Mongodb) receivedMongodbRequest(msg *MongodbMessage) {
	// Add it to the HT
	tuple := msg.TcpTuple

	trans := mongodb.transactionsMap[tuple.Hashable()]
	if trans != nil {
		if trans.Mongodb != nil {
			logp.Warn("Two requests without a Response. Dropping old request")
		}
	} else {
		logp.Debug("mongodb", "Initialize new transaction from request")
		trans = &MongodbTransaction{Type: "mongodb", tuple: tuple}
		mongodb.transactionsMap[tuple.Hashable()] = trans
	}

	trans.Mongodb = common.MapStr{}

	trans.event = msg.event

	trans.method = msg.method

	trans.cmdline = msg.CmdlineTuple
	trans.ts = msg.Ts
	trans.Ts = int64(trans.ts.UnixNano() / 1000) // transactions have microseconds resolution
	trans.JsTs = msg.Ts
	trans.Src = common.Endpoint{
		Ip:   msg.TcpTuple.Src_ip.String(),
		Port: msg.TcpTuple.Src_port,
		Proc: string(msg.CmdlineTuple.Src),
	}
	trans.Dst = common.Endpoint{
		Ip:   msg.TcpTuple.Dst_ip.String(),
		Port: msg.TcpTuple.Dst_port,
		Proc: string(msg.CmdlineTuple.Dst),
	}
	if msg.Direction == tcp.TcpDirectionReverse {
		trans.Src, trans.Dst = trans.Dst, trans.Src
	}

	if trans.timer != nil {
		trans.timer.Stop()
	}
	trans.timer = time.AfterFunc(TransactionTimeout, func() { mongodb.expireTransaction(trans) })

}

func (mongodb *Mongodb) expireTransaction(trans *MongodbTransaction) {
	logp.Debug("mongodb", "Expire transaction")
	// remove from map
	delete(mongodb.transactionsMap, trans.tuple.Hashable())
}

func (mongodb *Mongodb) receivedMongodbResponse(msg *MongodbMessage) {

	tuple := msg.TcpTuple
	trans := mongodb.transactionsMap[tuple.Hashable()]
	if trans == nil {
		logp.Warn("Response from unknown transaction. Ignoring.")
		return
	}
	// check if the request was received
	if trans.Mongodb == nil {
		logp.Warn("Response from unknown transaction. Ignoring.")
		return

	}

	// Merge request and response events attributes
	for k, v := range msg.event {
		trans.event[k] = v
	}

	trans.error = msg.error

	trans.ResponseTime = int32(msg.Ts.Sub(trans.ts).Nanoseconds() / 1e6) // resp_time in milliseconds

	mongodb.publishTransaction(trans)

	logp.Debug("mongodb", "Mongodb transaction completed: %s", trans.Mongodb)

	// remove from map
	delete(mongodb.transactionsMap, trans.tuple.Hashable())
	if trans.timer != nil {
		trans.timer.Stop()
	}
}

func (mongodb *Mongodb) GapInStream(tcptuple *common.TcpTuple, dir uint8,
	private protos.ProtocolData) protos.ProtocolData {

	// TODO

	return private
}

func (mongodb *Mongodb) ReceivedFin(tcptuple *common.TcpTuple, dir uint8,
	private protos.ProtocolData) protos.ProtocolData {

	// TODO
	return private
}

func (mongodb *Mongodb) publishTransaction(t *MongodbTransaction) {

	if mongodb.results == nil {
		logp.Debug("mongodb", "Try to publish transaction with null results")
		return
	}

	event := common.MapStr{}
	event["type"] = "mongodb"
	if t.error == "" {
		event["status"] = common.OK_STATUS
	} else {
		t.event["error"] = t.error
		event["status"] = common.ERROR_STATUS
	}
	event["mongodb"] = t.event
	event["method"] = t.method
	fullCollectioName, _ := t.event["fullCollectionName"].(string)
	event["query"] = t.method + " " + fullCollectioName
	event["responsetime"] = t.ResponseTime
	event["bytes_in"] = uint64(t.BytesIn)
	event["bytes_out"] = uint64(t.BytesOut)
	event["@timestamp"] = common.Time(t.ts)
	event["src"] = &t.Src
	event["dst"] = &t.Dst

	mongodb.results <- event
}
