package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"time"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"
)

var (
	cfgPath = flag.String("config", "config.yml", "path to config file for the simulation")
)

type Config struct {
	// Number of shards in the simulation.
	NumShards uint16 `yaml:"num-shards"`
	// Upper bound on cross-shard tx latency enforced by our node.
	Delta time.Duration `yaml:"delta"`
	// Duration between produced txs.
	TxProductionDelay time.Duration `yaml:"tx-production-delay"`
	// Processing latency to simulate STF processing times in shards.
	AvgShardTxProcessingLatency time.Duration `yaml:"avg-shard-tx-processing-latency"`
	// Size of each shard's processing buffer before it is "at capacity".
	ShardQueueBufferSize    uint64  `yaml:"shard-queue-buffer-size"`
	ProbabilityOfCrossShard float32 `yaml:"probability-of-cross-shard"`
	// Simulation duration.
	SimulationDuration time.Duration `yaml:"simulation-duration"`
	// When to report metrics of the system.
	MetricsReportingInterval time.Duration `yaml:"metrics-reporting-interval"`
}

type shardIndex uint16

type transaction struct {
	shard        shardIndex
	isCrossShard bool
	toShard      shardIndex
	timestamp    time.Time
}

type shard struct {
	cfg                            *Config
	idx                            shardIndex
	stateRoot                      [32]byte
	processingQueue                chan *transaction
	intakeQueue                    chan *transaction
	futureTxsQueue                 []*transaction
	latestClearedTxTimestamp       time.Time
	timesDispatchSucceeded         uint64
	timesDispatchFailed            uint64
	totalProcessed                 uint64
	totalClearedFromFutureTxsQueue uint64
	crossShardTxTotal              uint64
}

type sim struct {
	cfg    *Config
	shards map[shardIndex]*shard
}

func newSim(cfg *Config) *sim {
	// Initialize shards with their own processing queues.
	shards := make(map[shardIndex]*shard)
	for i := uint16(0); i < cfg.NumShards; i++ {
		shards[shardIndex(i)] = &shard{
			cfg:             cfg,
			idx:             shardIndex(i),
			stateRoot:       [32]byte{},
			futureTxsQueue:  make([]*transaction, 0),
			processingQueue: make(chan *transaction, cfg.ShardQueueBufferSize),
			intakeQueue:     make(chan *transaction, 1),
		}
	}
	return &sim{
		cfg:    cfg,
		shards: shards,
	}
}

func main() {
	flag.Parse()
	cfg, err := unmarshalConfig(*cfgPath)
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.SimulationDuration)
	defer cancel()

	// Run the simulation in the background with a default duration.
	go run(ctx, newSim(cfg))

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		panic(err)
	}
	defer watcher.Close()

	done := make(chan bool)
	go func() {
		defer close(done)
		for {
			select {
			case _, ok := <-watcher.Events:
				if !ok {
					return
				}
				cfg, err := unmarshalConfig(*cfgPath)
				if err != nil {
					panic(err)
				}
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), cfg.SimulationDuration)
				go run(ctx, newSim(cfg))
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				panic(err)
			}
		}

	}()

	if err = watcher.Add(*cfgPath); err != nil {
		panic(err)
	}
	<-done
}

func run(ctx context.Context, s *sim) {
	go s.reportMetrics(ctx)

	// Run each shard as a goroutine waiting to receive dispatched txs to process them as needed.
	for _, shrd := range s.shards {
		go shrd.runTransactionIntake(ctx)
		go shrd.runTransactionProcessing(ctx)
	}

	s.produceTxs(ctx)
}

func (s *sim) reportMetrics(ctx context.Context) {
	tick := time.NewTicker(s.cfg.MetricsReportingInterval)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			percentUsed := make([]string, s.cfg.NumShards)
			totalProcessed := make([]uint64, s.cfg.NumShards)
			crossShardTxPercent := make([]string, s.cfg.NumShards)
			futureTxsQueueLens := make([]int, s.cfg.NumShards)
			failRates := make([]string, s.cfg.NumShards)
			for i := 0; i < len(percentUsed); i++ {
				srd := s.shards[shardIndex(i)]
				pct := float64(len(srd.processingQueue)) / float64(s.cfg.ShardQueueBufferSize)
				percentUsed[i] = fmt.Sprintf("%.2f", pct)
				totalProcessed[i] = srd.totalProcessed
				crossShardPct := float64(srd.crossShardTxTotal) / float64(srd.totalProcessed)
				crossShardTxPercent[i] = fmt.Sprintf("%.2f", crossShardPct)
				futureTxsQueueLens[i] = len(srd.futureTxsQueue)
				failRatePct := float64(srd.timesDispatchFailed) / float64(srd.timesDispatchSucceeded+srd.timesDispatchFailed)
				failRates[i] = fmt.Sprintf("%.2f", failRatePct)
			}
			// ANSI screen clear code.
			fmt.Print("\033[H\033[2J")
			fmt.Printf("Num shards=%d\n", s.cfg.NumShards)
			fmt.Printf("Delta=%v\n", s.cfg.Delta)
			fmt.Printf("Txs produced per second=%d\n", time.Second/s.cfg.TxProductionDelay)
			fmt.Printf("Individual shard buffer size=%d\n", s.cfg.ShardQueueBufferSize)
			fmt.Printf("Avg shard tx processing latency=%v\n", s.cfg.AvgShardTxProcessingLatency)
			fmt.Printf("Shard capacity used=%v\n", percentUsed)
			fmt.Printf("Dispatch tx to shard failure rate=%v\n", failRates)
			fmt.Printf("Total txs processed per shard=%v\n", totalProcessed)
			fmt.Printf("Pending (future) transactions per shard=%v\n", futureTxsQueueLens)
			fmt.Printf("Percentage of txs cross-shard=%v\n", crossShardTxPercent)
		case <-ctx.Done():
			return
		}
	}
}

func (s *sim) produceTxs(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			time.Sleep(s.cfg.TxProductionDelay)

			// TODO: Currently uniform distribution across shards, should vary.
			shardIdx := rand.Int31n(int32(s.cfg.NumShards))
			t := &transaction{
				shard:     shardIndex(shardIdx),
				timestamp: time.Now(),
			}
			if shouldBeCrossShard(s.cfg) {
				// Cross shard transactions should be processed at T + delta, where delta is
				// a configuration parameter of our system.
				t.timestamp = t.timestamp.Add(s.cfg.Delta)
				toShardIdx := rand.Int31n(int32(s.cfg.NumShards))
				t.isCrossShard = true
				t.toShard = shardIndex(toShardIdx)
			}

			srd := s.shards[t.shard]
			if canSendTxToShard(s.cfg, t, srd) {
				srd.intakeQueue <- t
			}
		}
	}
}

// Decides what a shard should do when a transaction is received.
func (s *shard) runTransactionIntake(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			t := <-s.intakeQueue

			// When a shard receives a tx, it is either ready to process right away
			// or in the future. If it is in the future, we put it in a "pending"
			// queue which should include cross-shard txs.
			if t.timestamp.After(time.Now()) {
				s.futureTxsQueue = append(s.futureTxsQueue, t)
				continue
			}

			// Otherwise, we send it to a processing queue for immediate consideration.
			s.processingQueue <- t
		}
	}
}

func (s *shard) runTransactionProcessing(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			t := <-s.processingQueue
			// Process the incoming transaction normally.
			s.processTx(t)
			// If we can process from the future txs queue, do it now.
			s.processQueuedFutureTxs()
		}
	}
}

func (s *shard) processTx(tx *transaction) {
	// Process the transaction and simulate latency from normal processing.
	time.Sleep(s.cfg.AvgShardTxProcessingLatency)

	// Update the latest cleared tx timestamp for the shard for the dispatcher
	// to know if it can send more transactions over to the shard's processing queue.
	s.latestClearedTxTimestamp = tx.timestamp
	s.totalProcessed++
	if tx.isCrossShard {
		s.crossShardTxTotal++
	}
}

// Processes transactions from the future queue if possible, and clears
// out the queue as it goes along.
func (s *shard) processQueuedFutureTxs() {
	for len(s.futureTxsQueue) > 0 {
		item := s.futureTxsQueue[0]
		// If the transaction is in the future and cannot yet be processed,
		// we skip our processing for now.
		if item.timestamp.After(time.Now()) {
			return
		}
		s.processTx(item)
		s.futureTxsQueue = s.futureTxsQueue[1:]
		s.totalClearedFromFutureTxsQueue++
	}
}

func shouldBeCrossShard(cfg *Config) bool {
	return rand.Float32() < cfg.ProbabilityOfCrossShard
}

// Can only send a transaction with timestamp T to a shard if all transactions
// at T - delta have been retired from the shard, or if a shard's processing queue is empty.
func canSendTxToShard(cfg *Config, t *transaction, shrd *shard) bool {
	if len(shrd.processingQueue) == 0 {
		shrd.timesDispatchSucceeded++
		return true
	}
	mustClearThreshold := t.timestamp.Add(-cfg.Delta)
	if shrd.latestClearedTxTimestamp.After(mustClearThreshold) {
		shrd.timesDispatchSucceeded++
		return true
	}
	shrd.timesDispatchFailed++
	return false
}

func unmarshalConfig(configFile string) (*Config, error) {
	f, err := os.Open(configFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	enc, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	c := &Config{}
	if err := yaml.Unmarshal(enc, c); err != nil {
		return nil, err
	}
	return c, nil
}
