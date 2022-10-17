# Layer 2 Sharding Simulation

This project runs a simple Go binary that runs a simulation of layer 2 cross-shard transactions. It uses
a configuration file to run a simulation that spins up N shards and generates T transactions per second that are
dispatched to each shard. With some probability, some transactions can be a special-type, known as a "cross-shard transaction",
which are timestamped at some point in the future. This simulation attempts to spam each corresponding shard
with a corresponding, generated transaction, and measures different parts of the system.

## Goals

The goal of the simulation is to create a fairly realistic example of N shards running transaction processing routines
and being able to dispatch transactions to them and observe different behaviors at scale. Go makes this really easy
through the use of goroutines and channels, and we use simple time.Sleep calls to simulate latencies in transaction
processing and more.

With this particular tool, we want to better understand optimal parameters that govern cross-shard transactions
in our system in order to make good trade-offs for users and safety of the protocol.

## Running

Install at least Go 1.18, then do

```
git clone https://github.com/rauljordan/layer2shards && cd layer2shards
go run main.go
```

**You can live edit the config.yml file and save to automatically restart the simulation**

## How it works

1. Initializes N shards and runs their transaction processing routines in the background as goroutines
2. Starts a transaction generator which produces T transactions per second that are each meant for a shard. These are uniformly distributed across shards
3. With some probability `P`, it generates a "cross-shard" transaction which is timestamped at time T + delta, where delta is a parameter of our simulation set to 1s by default. Transactions must be processed in order of timestamp by each shard, so when a "cross-shard" transaction is received by a shard, it is placed in some future queue for later processing
4. Shards process transactions as fast as they possibly can, with each shard processing approx 84 txs / sec (roughly 7x Ethereums). Each transaction is processed in order of timestamp
5. We log to stdout data of the system including shard capacity at use, "pending" transactions per shard, and more
6. The simulation can be live-restarted by editing the configuration file on the fly

Here's the sample output:

```
Num shards=8
Delta=1s
Txs produced per second=1000
Individual shard buffer size=100
Avg shard tx processing latency=12ms
Shard capacity used=[0.08 0.00 0.10 0.02 0.05 0.21 0.18 0.00]
Dispatch tx to shard failure rate=[0.30 0.27 0.31 0.25 0.32 0.31 0.34 0.30]
Total txs processed per shard=[204 191 203 196 209 221 212 206]
Pending (future) transactions per shard=[0 15 8 18 0 0 0 2]
Percentage of txs cross-shard=[0.13 0.20 0.10 0.09 0.13 0.12 0.12 0.13]
```

## Parameters

The available parameters and default values used are kept in a `config.yml` file, which can be live-edited
when running the simulation to restart it as needed

```yaml
num-shards: 8 # Shards in the simulation

# Latencies
delta: 1s # Parameter used to govern latency of cross-shard transactions
tx-production-delay: 1ms # How often to produce one tx (1000 txs per second)
avg-shard-tx-processing-latency: 12ms # Time it takes each shard to process 1 tx (roughly 84 txs/sec)

# Capacities
shard-queue-buffer-size: 100 # Number of txs shard can hold in its queue at a time before it gets full

# Simulation tweaks
probability-of-cross-shard: 0.1 # Probability a generated tx will be "cross-shard"
simulation-duration: 5m # Runs for 5 minutes before exiting
metrics-reporting-interval: 100ms # Prints metrics to stdout every 100ms
```

## Questions

- How can we better understand delta? What exactly do we want to measure from changing these parameters?
- Is the system set up correctly? Is this simulation working the way we intend it to?
- Can we apply queuing theory to this simulation?
- How can we extend this simulation to be more realistic?
- What is a good way of gathering / plotting the data from this simulation?

## Ideas

- We should incorporate some notion of pricing as a way of determining shard capacity, and introduce something akin to the L2 pricing model!