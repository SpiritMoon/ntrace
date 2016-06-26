package tcpassembly

import (
	"bitbucket.org/zhengyuli/ntrace/analyzer"
	"bitbucket.org/zhengyuli/ntrace/decode"
	"bitbucket.org/zhengyuli/ntrace/layers"
	"container/list"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"math"
	"time"
)

func seqDiff(x, y uint32) int {
	if x > math.MaxUint32-math.MaxUint32/4 && y < math.MaxUint32/4 {
		return int(int64(x) - int64(y) - math.MaxUint32)
	} else if x < math.MaxUint32/4 && y > math.MaxUint32-math.MaxUint32/4 {
		return int(int64(x) + math.MaxUint32 - int64(y))
	}

	return int(int64(x) - int64(y))
}

type Direction uint8

const (
	FromClient Direction = iota
	FromServer
)

func (d Direction) String() string {
	if d == FromClient {
		return "FromClient"
	}

	return "FromServer"
}

type TcpState uint16

const (
	TcpSynSent TcpState = iota
	TcpSynReceived
	TcpEstablished
	TcpFinSent
	TcpFinConfirmed
	TcpClosing
	TcpClosed
)

type Tuple4 struct {
	SrcIP   string
	SrcPort uint16
	DstIP   string
	DstPort uint16
}

func (t Tuple4) String() string {
	return fmt.Sprintf("%s:%d-%s:%d", t.SrcIP, t.SrcPort, t.DstIP, t.DstPort)
}

type Page struct {
	Seq      uint32
	Ack      uint32
	URG, FIN bool
	Urgent   uint16
	Payload  []byte
}

type HalfStream struct {
	State     TcpState
	Seq       uint32
	Ack       uint32
	ExpRcvSeq uint32
	RecvData  []byte
	Pages     list.List
}

type StreamState uint16

const (
	StreamConnecting StreamState = iota
	StreamConnected
	StreamDataExchanging
	StreamClosing
	StreamClosingTimeout
	StreamClosed
	StreamClosedAbnormally
	StreamClosedExceedMaxCount
	StreamResetByClientBeforeConn
	StreamResetByServerBeforeConn
	StreamResetByClientAferConn
	StreamResetByServerAferConn
)

type Stream struct {
	Addr                      Tuple4
	State                     StreamState
	Client                    HalfStream
	Server                    HalfStream
	StreamsListElement        *list.Element
	ClosingExpireTime         time.Time
	ClosingStreamsListElement *list.Element
	Analyzer                  analyzer.Analyzer
}

type Assembler struct {
	Count              uint32
	Streams            map[Tuple4]*Stream
	StreamsList        list.List
	ClosingStreamsList list.List
}

func (a *Assembler) handleEstb(stream *Stream, timestamp time.Time) {
	log.Debugf("Tcp assembly: tcp connection %s connect.", stream.Addr)

	stream.Client.State = TcpEstablished
	stream.Server.State = TcpEstablished
	stream.State = StreamConnected
}

func (a *Assembler) handleData(stream *Stream, snd *HalfStream, rcv *HalfStream, timestamp time.Time) {
	var direction Direction
	if snd == &stream.Client {
		direction = FromClient
	} else {
		direction = FromServer
	}

	log.Debugf("Tcp assembly: tcp connection %s get %d bytes data %s - %s.", stream.Addr, len(rcv.RecvData), direction, rcv.RecvData)

	rcv.RecvData = make([]byte, 0, 4096)
}

func (a *Assembler) handleReset(stream *Stream, snd *HalfStream, rcv *HalfStream, timestamp time.Time) {
	var direction Direction
	if snd == &stream.Client {
		direction = FromClient
	} else {
		direction = FromServer
	}

	log.Warnf("Tcp assembly: tcp connection %s reset %s.", stream.Addr, direction)

	if stream.State == StreamConnecting {
		if direction == FromClient {
			stream.State = StreamResetByClientBeforeConn
		} else {
			stream.State = StreamResetByServerBeforeConn
		}
	} else {
		if direction == FromClient {
			stream.State = StreamResetByClientAferConn
		} else {
			stream.State = StreamResetByServerAferConn
		}
	}

	a.removeStream(stream)
}

func (a *Assembler) handleFin(stream *Stream, snd *HalfStream, rcv *HalfStream, timestamp time.Time, lazyMode bool) {
	var direction Direction
	if snd == &stream.Client {
		direction = FromClient
	} else {
		direction = FromServer
	}

	log.Debugf("Tcp assembly: tcp connection %s get fin packet %s in lazyMode=%t.", stream.Addr, direction, lazyMode)

	if !lazyMode {
		snd.State = TcpFinSent
	}
	stream.State = StreamClosing
	a.addClosingStream(stream, timestamp)
}

func (a *Assembler) handleClose(stream *Stream, timestamp time.Time) {
	log.Debugf("Tcp assembly: tcp connection %s close normally.", stream.Addr)

	stream.State = StreamClosed
	a.removeStream(stream)
}

func (a *Assembler) handleCloseAbnormally(stream *Stream, timestamp time.Time) {
	log.Errorf("Tcp assembly: tcp connection %s close abnormally.", stream.Addr)

	stream.State = StreamClosedAbnormally
	a.removeStream(stream)
}

func (a *Assembler) handleCloseExceedMaxCount(stream *Stream, timestamp time.Time) {
	log.Warnf("Tcp assembly: tcp connection %s close exceed max count.", stream.Addr)

	stream.State = StreamClosedExceedMaxCount
	a.removeStream(stream)
}

func (a *Assembler) handleClosingTimeout(stream *Stream, timestamp time.Time) {
	log.Errorf("Tcp assembly: tcp connection %s close timeout.", stream.Addr)

	stream.State = StreamClosingTimeout
	a.removeStream(stream)
}

func (a *Assembler) findStream(ipDecoder *layers.IPv4, tcpDecoder *layers.TCP) (*Stream, Direction) {
	stream := a.Streams[Tuple4{
		SrcIP:   ipDecoder.SrcIP.String(),
		SrcPort: tcpDecoder.SrcPort,
		DstIP:   ipDecoder.DstIP.String(),
		DstPort: tcpDecoder.DstPort}]
	if stream != nil {
		return stream, FromClient
	}

	stream = a.Streams[Tuple4{
		SrcIP:   ipDecoder.DstIP.String(),
		SrcPort: tcpDecoder.DstPort,
		DstIP:   ipDecoder.SrcIP.String(),
		DstPort: tcpDecoder.SrcPort}]
	if stream != nil {
		return stream, FromServer
	}

	return nil, FromClient
}

func (a *Assembler) addStream(ipDecoder *layers.IPv4, tcpDecoder *layers.TCP, timestamp time.Time) {
	addr := Tuple4{
		SrcIP:   ipDecoder.SrcIP.String(),
		SrcPort: tcpDecoder.SrcPort,
		DstIP:   ipDecoder.DstIP.String(),
		DstPort: tcpDecoder.DstPort}
	stream := &Stream{
		Addr:  addr,
		State: StreamConnecting,
		Client: HalfStream{
			State:    TcpSynSent,
			Seq:      tcpDecoder.Seq,
			Ack:      tcpDecoder.Ack,
			RecvData: make([]byte, 0, 4096),
		},
		Server: HalfStream{
			State:     TcpClosed,
			ExpRcvSeq: tcpDecoder.Seq + 1,
			RecvData:  make([]byte, 0, 4096),
		},
	}
	a.Count++
	a.Streams[addr] = stream
	stream.StreamsListElement = a.StreamsList.PushBack(stream)

	for a.StreamsList.Len() > 65535 {
		stream := a.StreamsList.Front().Value.(*Stream)
		a.handleCloseAbnormally(stream, timestamp)
	}
}

func (a *Assembler) removeStream(stream *Stream) {
	delete(a.Streams, stream.Addr)
	a.StreamsList.Remove(stream.StreamsListElement)
	if stream.ClosingStreamsListElement != nil {
		a.ClosingStreamsList.Remove(stream.ClosingStreamsListElement)
	}
}

func (a *Assembler) addClosingStream(stream *Stream, timestamp time.Time) {
	stream.ClosingExpireTime = timestamp.Add(time.Second * 30)

	if stream.ClosingStreamsListElement != nil {
		a.ClosingStreamsList.Remove(stream.ClosingStreamsListElement)
	}
	stream.ClosingStreamsListElement = a.ClosingStreamsList.PushBack(stream)
}

func (a *Assembler) checkClosingStream(timestamp time.Time) {
	for a.ClosingStreamsList.Len() > 0 {
		stream := a.ClosingStreamsList.Front().Value.(*Stream)
		if timestamp.Before(stream.ClosingExpireTime) {
			break
		}

		a.handleClosingTimeout(stream, timestamp)
	}
}

func (a *Assembler) addFromPage(stream *Stream, snd *HalfStream, rcv *HalfStream, page *Page, timestamp time.Time) {
	if page.URG {
		if seqDiff(page.Seq+uint32(page.Urgent-1), rcv.ExpRcvSeq) >= 0 {
			rcv.RecvData = append(
				rcv.RecvData,
				page.Payload[seqDiff(rcv.ExpRcvSeq, page.Seq):page.Urgent-1]...)
			rcv.RecvData = append(
				rcv.RecvData,
				page.Payload[page.Urgent:]...)
		} else {
			rcv.RecvData = append(
				rcv.RecvData,
				page.Payload[seqDiff(rcv.ExpRcvSeq, page.Seq):]...)
		}
	} else {
		rcv.RecvData = append(
			rcv.RecvData,
			page.Payload[seqDiff(rcv.ExpRcvSeq, page.Seq):]...)
	}

	rcv.ExpRcvSeq = page.Seq + uint32(len(page.Payload))
	if page.FIN {
		rcv.ExpRcvSeq++
	}

	if page.FIN {
		a.handleFin(stream, snd, rcv, timestamp, false)
	}
}

func (a *Assembler) tcpQueue(stream *Stream, snd *HalfStream, rcv *HalfStream, tcpDecoder *layers.TCP, timestamp time.Time) {
	page := &Page{
		Seq:     tcpDecoder.Seq,
		Ack:     tcpDecoder.Ack,
		URG:     tcpDecoder.URG,
		FIN:     tcpDecoder.FIN,
		Urgent:  tcpDecoder.Urgent,
		Payload: tcpDecoder.Payload,
	}

	if seqDiff(tcpDecoder.Seq, rcv.ExpRcvSeq) <= 0 {
		endSeq := tcpDecoder.Seq + uint32(len(tcpDecoder.Payload))
		if tcpDecoder.FIN {
			endSeq++
		}

		if seqDiff(endSeq, rcv.ExpRcvSeq) <= 0 {
			log.Debug("Tcp assembly: tcp connection %s get retransmited packet %s.", stream.Addr)
			return
		}

		a.addFromPage(stream, snd, rcv, page, timestamp)
		for e := rcv.Pages.Front(); e != nil; {
			if seqDiff(e.Value.(*Page).Seq, rcv.ExpRcvSeq) > 0 {
				break
			}

			a.addFromPage(stream, snd, rcv, page, timestamp)
			tmp := e.Next()
			rcv.Pages.Remove(e)
			e = tmp
		}
		a.handleData(stream, snd, rcv, timestamp)
	} else {
		var e *list.Element
		for e = rcv.Pages.Front(); e != nil; e = e.Next() {
			if seqDiff(e.Value.(*Page).Seq, tcpDecoder.Seq) > 0 {
				rcv.Pages.InsertBefore(page, e)
				break
			}
		}
		if e == nil {
			rcv.Pages.PushBack(page)
		}

		if tcpDecoder.FIN {
			a.handleFin(stream, snd, rcv, timestamp, true)
		}
	}
}

func (a *Assembler) Assemble(context *decode.Context) {
	ipDecoder := context.NetworkDecoder.(*layers.IPv4)
	tcpDecoder := context.TransportDecoder.(*layers.TCP)

	stream, direction := a.findStream(ipDecoder, tcpDecoder)
	if stream == nil {
		// The first packet of tcp three handshakes
		if tcpDecoder.SYN && !tcpDecoder.ACK && !tcpDecoder.RST {
			a.addStream(ipDecoder, tcpDecoder, context.Time)
		}
		return
	}

	if tcpDecoder.SYN {
		// The second packet of tcp three handshakes
		if direction == FromServer && tcpDecoder.ACK &&
			stream.Client.State == TcpSynSent && stream.Server.State == TcpClosed {
			stream.Server.State = TcpSynReceived
			stream.Server.Seq = tcpDecoder.Seq
			stream.Server.Ack = tcpDecoder.Ack
			stream.Client.ExpRcvSeq = tcpDecoder.Seq + 1
			return
		}

		// Tcp sync retries
		if direction == FromClient &&
			stream.Client.State == TcpSynSent {
			log.Debug("tmp - Tcp syn retries.")
			return
		}

		// Tcp sync/ack retries
		if direction == FromServer &&
			stream.Server.State == TcpSynReceived {
			log.Debug("tmp - Tcp syn/ack retries.")
			return
		}

		// Unlikely, invalid packet with syn
		a.handleCloseAbnormally(stream, context.Time)
		return
	}

	var snd, rcv *HalfStream
	if direction == FromClient {
		snd = &stream.Client
		rcv = &stream.Server
	} else {
		snd = &stream.Server
		rcv = &stream.Client
	}
	snd.Seq = tcpDecoder.Seq

	// Tcp rset packet
	if tcpDecoder.RST {
		a.handleReset(stream, snd, rcv, context.Time)
		return
	}

	if tcpDecoder.ACK {
		// The third packet of tcp three handshakes
		if direction == FromClient &&
			stream.Client.State == TcpSynSent && stream.Server.State == TcpSynReceived {
			if tcpDecoder.Seq != stream.Server.ExpRcvSeq {
				log.Debug("Tcp assembly: unexpected sequence=%d of the third packet of "+
					"tcp three handshakes, expected sequence=%d.", tcpDecoder.Seq, stream.Server.ExpRcvSeq)
				a.handleCloseAbnormally(stream, context.Time)
				return
			}

			a.handleEstb(stream, context.Time)
		}

		if seqDiff(snd.Ack, tcpDecoder.Ack) < 0 {
			snd.Ack = tcpDecoder.Ack
		} else {
			log.Debug("tmp - Duplicated ack sequence.")
		}

		if rcv.State == TcpFinSent {
			rcv.State = TcpFinConfirmed
		}

		if snd.State == TcpFinConfirmed && rcv.State == TcpFinConfirmed {
			a.handleClose(stream, context.Time)
			return
		}
	}

	if len(tcpDecoder.Payload) > 0 || tcpDecoder.FIN {
		if stream.State != StreamDataExchanging {
			stream.State = StreamDataExchanging
		}

		if len(tcpDecoder.Payload) > 0 && len(tcpDecoder.Payload) <= 32 {
			log.Debug("tmp - tiny packets.")
		}

		a.tcpQueue(stream, snd, rcv, tcpDecoder, context.Time)
	}
}

func NewAssembler() *Assembler {
	return &Assembler{
		Streams: make(map[Tuple4]*Stream),
	}
}