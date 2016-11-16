package main

import (
	"encoding/json"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/zhengyuli/ntrace/layers"
	"github.com/zhengyuli/ntrace/sniffer"
	"github.com/zhengyuli/ntrace/sniffer/driver"
	"github.com/zhengyuli/ntrace/tcpassembly"
	"hash/fnv"
	"net"
	"os"
	"os/signal"
	"path"
	"reflect"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type RunState uint32

func (rs *RunState) stop() {
	atomic.StoreUint32((*uint32)(rs), 1)
}

func (rs *RunState) stopped() bool {
	s := atomic.LoadUint32((*uint32)(rs))

	if s > 0 {
		return true
	}

	return false
}

var (
	netDev   string
	logDir   string
	logFile  string
	logLevel log.Level

	runState RunState
)

func init() {
	netDev = "en0"
	logDir = "/var/log"
	logFile = "ntrace"
	logLevel = log.DebugLevel
}

func setupTeardown() {
	sigChannel := make(chan os.Signal)
	signal.Notify(sigChannel, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sigChannel
		runState.stop()
		signal.Stop(sigChannel)
		close(sigChannel)
	}()
}

func setupLogger(logDir string, logFile string, logLevel log.Level) (*os.File, error) {
	err := os.MkdirAll(logDir, 0755)

	if err != nil {
		return nil, err
	}

	if path.Ext(logFile) != ".log" {
		logFile = logFile + ".log"
	}
	logFilePath := path.Join(logDir, logFile)
	out, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return nil, err
	}
	log.SetOutput(out)

	log.SetFormatter(
		&log.TextFormatter{
			FullTimestamp:  true,
			DisableColors:  true,
			DisableSorting: true})
	log.SetLevel(logLevel)

	return out, nil
}

func datalinkCaptureService(netDev string, ipDispatchChannel chan *layers.Packet, wg *sync.WaitGroup) {
	defer func() {
		wg.Done()
	}()

	handle, err := sniffer.New(netDev)
	if err != nil {
		panic(err)
	}

	err = handle.SetFilter("tcp or icmp")
	if err != nil {
		panic(err)
	}

	pkt := new(driver.Packet)
	for !runState.stopped() {
		err := handle.NextPacket(pkt)
		if err != nil {
			panic(err)
		}

		if pkt.Data != nil {
			// Filter out incomplete network packet
			if pkt.CapLen != pkt.PktLen {
				log.Warn("Incomplete packet.")
				continue
			}

			layerType := handle.DatalinkType()
			decoder := layerType.NewDecoder()
			if decoder == nil {
				panic(fmt.Errorf("No proper decoder for %s.", layerType.Name()))
			}
			if err = decoder.Decode(pkt.Data); err != nil {
				log.Errorf("Decode %s error: %s.", layerType.Name(), err)
				continue
			}

			packet := new(layers.Packet)
			packet.Time = pkt.Time
			packet.DatalinkDecoder = decoder

			switch decoder.NextLayerType() {
			case layers.ProtocolFamilyIPv4,
				layers.EthernetTypeIPv4:
				ipDispatchChannel <- packet

			default:
				log.Errorf("Unsupported next layer type: %s.", decoder.NextLayerType().Name())
			}
		}
	}
}

func ipProcessService(ipDispatchChannel chan *layers.Packet, icmpDispatchChannel chan *layers.Packet, tcpDispatchChannel chan *layers.Packet, wg *sync.WaitGroup) {
	defer func() {
		wg.Done()
	}()

	timer := time.NewTimer(time.Second)
	for !runState.stopped() {
		timer.Reset(time.Second)
		select {
		case packet := <-ipDispatchChannel:
			layerType := packet.DatalinkDecoder.NextLayerType()
			decoder := packet.DatalinkDecoder.NextLayerDecoder()
			if decoder == nil {
				log.Errorf("No proper decoder for %s.", layerType.Name())
				continue
			}
			if err := decoder.Decode(packet.DatalinkDecoder.LayerPayload()); err != nil {
				log.Errorf("Decode %s error: %s.", layerType.Name(), err)
				continue
			}

			packet.NetworkDecoder = decoder

			switch decoder.NextLayerType() {
			case layers.IPv4ProtocolICMP:
				icmpDispatchChannel <- packet

			case layers.IPv4ProtocolTCP:
				tcpDispatchChannel <- packet

			default:
				log.Errorf("Unsupported next layer type: %s.", decoder.NextLayerType())
			}

		case <-timer.C:
			break
		}
	}
}

func icmpProcessService(icmpDispatchChannel chan *layers.Packet, wg *sync.WaitGroup) {
	defer func() {
		wg.Done()
	}()

	timer := time.NewTimer(time.Second)
	for !runState.stopped() {
		timer.Reset(time.Second)
		select {
		case packet := <-icmpDispatchChannel:
			layerType := packet.NetworkDecoder.NextLayerType()
			decoder := packet.NetworkDecoder.NextLayerDecoder()
			if decoder == nil {
				log.Errorf("No proper decoder for %s.", layerType.Name())
				continue
			}
			if err := decoder.Decode(packet.NetworkDecoder.LayerPayload()); err != nil {
				log.Errorf("Decode %s error: %s.", layerType.Name(), err)
				continue
			}
			log.Infof("%s", decoder)

		case <-timer.C:
			break
		}
	}
}

func tcpDispatchHash(srcIP net.IP, srcPort uint16, dstIP net.IP, dstPort uint16) uint32 {
	var data1 []byte
	data1 = append(data1, []byte(srcIP)...)
	data1 = strconv.AppendInt(data1, int64(srcPort), 10)

	var data2 []byte
	data2 = append(data2, []byte(dstIP)...)
	data2 = strconv.AppendInt(data2, int64(dstPort), 10)

	if len(data1) < len(data2) {
		tmp := data1
		data1 = data2
		data2 = tmp
	}

	for i := 0; i < len(data2); i++ {
		data1[i] = data1[i] ^ data2[i]
	}

	sum := fnv.New32()
	sum.Write(data1)
	return sum.Sum32()
}

func tcpProcessService(tcpDispatchChannel chan *layers.Packet, tcpAssemblyChannels []chan *layers.Packet, wg *sync.WaitGroup) {
	defer func() {
		wg.Done()
	}()

	var srcIP, dstIP net.IP
	timer := time.NewTimer(time.Second)
	tcpDispatchChannelNum := uint32(len(tcpAssemblyChannels))

	for !runState.stopped() {
		timer.Reset(time.Second)
		select {
		case packet := <-tcpDispatchChannel:
			layerType := packet.NetworkDecoder.NextLayerType()
			decoder := packet.NetworkDecoder.NextLayerDecoder()
			if decoder == nil {
				log.Errorf("No proper decoder for %s.", layerType.Name())
				continue
			}
			if err := decoder.Decode(packet.NetworkDecoder.LayerPayload()); err != nil {
				log.Errorf("Decode %s error: %s.", layerType.Name(), err)
				continue
			}

			packet.TransportDecoder = decoder

			if ip4, ok := packet.NetworkDecoder.(*layers.IPv4); ok {
				srcIP = ip4.SrcIP
				dstIP = ip4.DstIP
			} else {
				log.Errorf("Unsupported network decoder: %s.", reflect.TypeOf(packet.NetworkDecoder))
				continue
			}

			tcp := packet.TransportDecoder.(*layers.TCP)
			hash := tcpDispatchHash(srcIP, tcp.SrcPort, dstIP, tcp.DstPort)
			tcpAssemblyChannels[hash%tcpDispatchChannelNum] <- packet

		case <-timer.C:
			break
		}
	}
}

func tcpAssemblyService(index int, tcpAssemblyChannel chan *layers.Packet, sessionBreakdownDumpChannel chan interface{}, wg *sync.WaitGroup) {
	assembler := tcpassembly.NewAssembler()

	defer func() {
		log.Infof("tcpAssemblyService: %d got %d tcp streams.", index, assembler.Count)
		wg.Done()
	}()

	timer := time.NewTimer(time.Second)
	for !runState.stopped() {
		timer.Reset(time.Second)
		select {
		case packet := <-tcpAssemblyChannel:
			assembler.Assemble(packet.NetworkDecoder, packet.TransportDecoder, packet.Time)
			for i := 0; i < len(assembler.SessionBreakdowns); i++ {
				sessionBreakdownDumpChannel <- assembler.SessionBreakdowns[i]
			}
			assembler.SessionBreakdowns = assembler.SessionBreakdowns[len(assembler.SessionBreakdowns):]

		case <-timer.C:
			break
		}
	}
}

func sessionBreakdownDumpService(sessionBreakdownDumpChannel chan interface{}, wg *sync.WaitGroup) {
	defer func() {
		wg.Done()
	}()

	timer := time.NewTimer(time.Second)
	for !runState.stopped() {
		timer.Reset(time.Second)
		select {
		case sessionBreakdown := <-sessionBreakdownDumpChannel:
			if sessionBreakdownBuf, err := json.Marshal(sessionBreakdown); err == nil {
				fmt.Println(string(sessionBreakdownBuf))
			}

		case <-timer.C:
			break
		}
	}
}

func main() {
	setupTeardown()

	out, err := setupLogger(logDir, logFile, logLevel)
	if err != nil {
		log.Fatalf("Setup default logger with error: %s.", err)
	}
	defer out.Close()

	// cpuNum := runtime.NumCPU()
	cpuNum := 1

	log.Infof("Run with GOMAXPROCS=%d.", 2*cpuNum+1)
	runtime.GOMAXPROCS(2*runtime.NumCPU() + 1)

	ipDispatchChannel := make(chan *layers.Packet, 100000)
	defer close(ipDispatchChannel)

	icmpDispatchChannel := make(chan *layers.Packet, 100000)
	defer close(icmpDispatchChannel)

	tcpDispatchChannel := make(chan *layers.Packet, 100000)
	defer close(tcpDispatchChannel)

	tcpAssemblyChannels := make([]chan *layers.Packet, cpuNum)
	for i := 0; i < cpuNum; i++ {
		tcpAssemblyChannels[i] = make(chan *layers.Packet, 100000)
		defer close(tcpAssemblyChannels[i])
	}

	sessionBreakdownDumpChannel := make(chan interface{}, 100000)
	defer close(sessionBreakdownDumpChannel)

	var wg sync.WaitGroup
	wg.Add(5 + cpuNum)

	go datalinkCaptureService(netDev, ipDispatchChannel, &wg)
	go ipProcessService(ipDispatchChannel, icmpDispatchChannel, tcpDispatchChannel, &wg)
	go icmpProcessService(icmpDispatchChannel, &wg)
	go tcpProcessService(tcpDispatchChannel, tcpAssemblyChannels, &wg)
	for i := 0; i < cpuNum; i++ {
		go tcpAssemblyService(i, tcpAssemblyChannels[i], sessionBreakdownDumpChannel, &wg)
	}
	go sessionBreakdownDumpService(sessionBreakdownDumpChannel, &wg)

	wg.Wait()
}
