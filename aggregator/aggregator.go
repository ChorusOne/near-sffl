package aggregator

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"sync"
	"time"

	chainioavsregistry "github.com/Layr-Labs/eigensdk-go/chainio/clients/avsregistry"
	"github.com/Layr-Labs/eigensdk-go/chainio/clients/wallet"
	"github.com/Layr-Labs/eigensdk-go/chainio/txmgr"
	"github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/Layr-Labs/eigensdk-go/metrics"
	"github.com/Layr-Labs/eigensdk-go/services/avsregistry"
	blsagg "github.com/Layr-Labs/eigensdk-go/services/bls_aggregation"
	opinfoserv "github.com/Layr-Labs/eigensdk-go/services/operatorsinfo"
	"github.com/Layr-Labs/eigensdk-go/signerv2"
	eigentypes "github.com/Layr-Labs/eigensdk-go/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/NethermindEth/near-sffl/aggregator/database"
	"github.com/NethermindEth/near-sffl/aggregator/types"
	taskmanager "github.com/NethermindEth/near-sffl/contracts/bindings/SFFLTaskManager"
	"github.com/NethermindEth/near-sffl/core"
	"github.com/NethermindEth/near-sffl/core/chainio"
	"github.com/NethermindEth/near-sffl/core/config"
	"github.com/NethermindEth/near-sffl/core/safeclient"
	coretypes "github.com/NethermindEth/near-sffl/core/types"
	"github.com/NethermindEth/near-sffl/core/types/messages"
)

const (
	// number of blocks after which a task is considered expired
	// this hardcoded here because it's also hardcoded in the contracts, but should
	// ideally be fetched from the contracts
	taskChallengeWindowBlock = 100
	blockTimeSeconds         = 12 * time.Second
	avsName                  = "super-fast-finality-layer"
)

var (
	// RPC errors
	DigestError                    = errors.New("Failed to get message digest")
	TaskResponseDigestError        = errors.New("Failed to get task response digest")
	GetOperatorSetUpdateBlockError = errors.New("Failed to get operator set update block")

	// REST errors
	StateRootUpdateNotFoundError = errors.New("StateRootUpdate not found")
	StateRootAggNotFoundError    = errors.New("StateRootUpdate aggregation not found")
	OperatorSetNotFoundError     = errors.New("OperatorSetUpdate not found")
	OperatorAggNotFoundError     = errors.New("OperatorSetUpdate aggregation not found")
	CheckpointNotFoundError      = errors.New("CheckpointMessages not found")
)

type RpcAggregatorer interface {
	ProcessSignedCheckpointTaskResponse(signedCheckpointTaskResponse *messages.SignedCheckpointTaskResponse) error
	ProcessSignedStateRootUpdateMessage(signedStateRootUpdateMessage *messages.SignedStateRootUpdateMessage) error
	ProcessSignedOperatorSetUpdateMessage(signedOperatorSetUpdateMessage *messages.SignedOperatorSetUpdateMessage) error
	GetAggregatedCheckpointMessages(fromTimestamp, toTimestamp uint64) (*messages.CheckpointMessages, error)
	GetRegistryCoordinatorAddress(reply *string) error
}

type RestAggregatorer interface {
	GetStateRootUpdateAggregation(rollupId uint32, blockHeight uint64) (*types.GetStateRootUpdateAggregationResponse, error)
	GetOperatorSetUpdateAggregation(id uint64) (*types.GetOperatorSetUpdateAggregationResponse, error)
	GetCheckpointMessages(fromTimestamp, toTimestamp uint64) (*types.GetCheckpointMessagesResponse, error)
}

// Aggregator sends checkpoint tasks onchain, then listens for operator signed TaskResponses.
// It aggregates responses signatures, and if any of the TaskResponses reaches the QuorumThreshold for each quorum
// (currently we only use a single quorum of the ERC20Mock token), it sends the aggregated TaskResponse and signature onchain.
//
// The signature is checked in the BLSSignatureChecker.sol contract, which expects a
//
//	struct NonSignerStakesAndSignature {
//		uint32[] nonSignerQuorumBitmapIndices;
//		BN254.G1Point[] nonSignerPubkeys;
//		BN254.G1Point[] quorumApks;
//		BN254.G2Point apkG2;
//		BN254.G1Point sigma;
//		uint32[] quorumApkIndices;
//		uint32[] totalStakeIndices;
//		uint32[][] nonSignerStakeIndices; // nonSignerStakeIndices[quorumNumberIndex][nonSignerIndex]
//	}
//
// A task can only be responded onchain by having enough operators sign on it such that their stake in each quorum reaches the QuorumThreshold.
// In order to verify this onchain, the Registry contracts store the history of stakes and aggregate pubkeys (apks) for each operators and each quorum. These are
// updated everytime an operator registers or deregisters with the BLSRegistryCoordinatorWithIndices.sol contract, or calls UpdateStakes() on the StakeRegistry.sol contract,
// after having received new delegated shares or having delegated shares removed by stakers queuing withdrawals. Each of these pushes to their respective datatype array a new entry.
//
// This is true for quorumBitmaps (represent the quorums each operator is opted into), quorumApks (apks per quorum), totalStakes (total stake per quorum), and nonSignerStakes (stake per quorum per operator).
// The 4 "indices" in NonSignerStakesAndSignature basically represent the index at which to fetch their respective data, given a blockNumber at which the task was created.
// Note that different data types might have different indices, since for eg QuorumBitmaps are updated for operators registering/deregistering, but not for UpdateStakes.
// Thankfully, we have deployed a helper contract BLSOperatorStateRetriever.sol whose function getCheckSignaturesIndices() can be used to fetch the indices given a block number.
//
// The 4 other fields nonSignerPubkeys, quorumApks, apkG2, and sigma, however, must be computed individually.
// apkG2 and sigma are just the aggregated signature and pubkeys of the operators who signed the task response (aggregated over all quorums, so individual signatures might be duplicated).
// quorumApks are the G1 aggregated pubkeys of the operators who signed the task response, but one per quorum, as opposed to apkG2 which is summed over all quorums.
// nonSignerPubkeys are the G1 pubkeys of the operators who did not sign the task response, but were opted into the quorum at the blocknumber at which the task was created.
// Upon sending a task onchain (or receiving a CheckpointTaskCreated Event if the tasks were sent by an external task generator), the aggregator can get the list of all operators opted into each quorum at that
// block number by calling the getOperatorState() function of the BLSOperatorStateRetriever.sol contract.
type Aggregator struct {
	config               *config.Config
	logger               logging.Logger
	serverIpPortAddr     string
	restServerIpPortAddr string
	checkpointInterval   time.Duration
	avsWriter            chainio.AvsWriterer
	avsReader            chainio.AvsReaderer
	rollupBroadcaster    RollupBroadcasterer
	httpClient           safeclient.SafeClient
	wsClient             safeclient.SafeClient
	clock                core.Clock

	// TODO(edwin): once rpc & rest decouple from aggregator fome it with them
	registry           *prometheus.Registry
	metrics            metrics.Metrics
	aggregatorListener AggregatorEventListener

	taskBlsAggregationService              blsagg.BlsAggregationService
	stateRootUpdateBlsAggregationService   MessageBlsAggregationService
	operatorSetUpdateBlsAggregationService MessageBlsAggregationService
	tasks                                  map[coretypes.TaskIndex]taskmanager.CheckpointTask
	tasksLock                              sync.RWMutex
	taskResponses                          map[coretypes.TaskIndex]map[eigentypes.TaskResponseDigest]messages.CheckpointTaskResponse
	taskResponsesLock                      sync.RWMutex
	msgDb                                  database.Databaser
	stateRootUpdates                       map[coretypes.MessageDigest]messages.StateRootUpdateMessage
	stateRootUpdatesLock                   sync.RWMutex
	operatorSetUpdates                     map[coretypes.MessageDigest]messages.OperatorSetUpdateMessage
	operatorSetUpdatesLock                 sync.RWMutex
}

var _ core.Metricable = (*Aggregator)(nil)
var _ RpcAggregatorer = (*Aggregator)(nil)
var _ RestAggregatorer = (*Aggregator)(nil)

// NewAggregator creates a new Aggregator with the provided config.
func NewAggregator(
	// TODO: Remove `ctx` once OperatorsInfoServiceInMemory's API is changed and we can gracefully exit otherwise
	ctx context.Context,
	config *config.Config,
	registry *prometheus.Registry,
	logger logging.Logger,
) (*Aggregator, error) {
	ethHttpClient, err := core.CreateEthClientWithCollector(AggregatorNamespace, config.EthHttpRpcUrl, config.EnableMetrics, registry, logger)
	if err != nil {
		logger.Error("Cannot create http ethclient", "err", err)
		return nil, err
	}

	ethWsClient, err := core.CreateEthClientWithCollector(AggregatorNamespace, config.EthWsRpcUrl, config.EnableMetrics, registry, logger)
	if err != nil {
		logger.Error("Cannot create ws ethclient", "err", err)
		return nil, err
	}

	avsReader, err := chainio.BuildAvsReaderFromConfig(config, ethHttpClient, logger)
	if err != nil {
		logger.Error("Cannot create avsReader", "err", err)
		return nil, err
	}

	chainId, err := ethHttpClient.ChainID(ctx)
	if err != nil {
		logger.Error("Cannot get chainId", "err", err)
		return nil, err
	}

	signerConfig := signerv2.Config{PrivateKey: config.EcdsaPrivateKey}
	signerV2, _, err := signerv2.SignerFromConfig(signerConfig, chainId)
	if err != nil {
		panic(err)
	}

	txSender, err := wallet.NewPrivateKeyWallet(ethHttpClient, signerV2, config.AggregatorAddress, logger)
	if err != nil {
		logger.Error("Failed to create transaction sender", "err", err)
		return nil, err
	}

	txMgr := txmgr.NewSimpleTxManager(txSender, ethHttpClient, logger, config.AggregatorAddress).WithGasLimitMultiplier(1.5)

	// note that the subscriber needs a ws connection instead of http
	avsRegistryChainSubscriber, err := chainioavsregistry.BuildAvsRegistryChainSubscriber(common.HexToAddress(config.SFFLRegistryCoordinatorAddr.String()), ethWsClient, logger)
	if err != nil {
		logger.Error("Cannot create AvsRegistryChainSubscriber", "err", err)
		return nil, err
	}

	avsWriter, err := chainio.BuildAvsWriterFromConfig(txMgr, config, ethHttpClient, logger)
	if err != nil {
		logger.Error("Cannot create avsWriter", "err", err)
		return nil, err
	}

	avsSubscriber, err := chainio.BuildAvsSubscriber(config.SFFLRegistryCoordinatorAddr, config.OperatorStateRetrieverAddr, ethWsClient, logger)
	if err != nil {
		logger.Error("Cannot create AvsSubscriber", "err", err)
		return nil, err
	}

	msgDb, err := database.NewDatabase(config.AggregatorDatabasePath)
	if err != nil {
		logger.Error("Cannot create database", "err", err)
		return nil, err
	}

	rollupBroadcaster, err := NewRollupBroadcaster(ctx, avsReader, avsSubscriber, config.RollupsInfo, signerConfig, config.AggregatorAddress, logger)
	if err != nil {
		logger.Error("Cannot create rollup broadcaster", "err", err)
		return nil, err
	}

	operatorPubkeysService := opinfoserv.NewOperatorsInfoServiceInMemory(ctx, avsRegistryChainSubscriber, avsReader, logger)
	avsRegistryService := avsregistry.NewAvsRegistryServiceChainCaller(avsReader, operatorPubkeysService, logger)
	taskBlsAggregationService := blsagg.NewBlsAggregatorService(avsRegistryService, logger)
	stateRootUpdateBlsAggregationService := NewMessageBlsAggregatorService(avsRegistryService, ethHttpClient, logger)
	operatorSetUpdateBlsAggregationService := NewMessageBlsAggregatorService(avsRegistryService, ethHttpClient, logger)

	agg := &Aggregator{
		config:                                 config,
		logger:                                 logger,
		serverIpPortAddr:                       config.AggregatorServerIpPortAddr,
		restServerIpPortAddr:                   config.AggregatorRestServerIpPortAddr,
		checkpointInterval:                     config.AggregatorCheckpointInterval,
		avsWriter:                              avsWriter,
		avsReader:                              avsReader,
		rollupBroadcaster:                      rollupBroadcaster,
		httpClient:                             ethHttpClient,
		wsClient:                               ethWsClient,
		clock:                                  core.SystemClock,
		taskBlsAggregationService:              taskBlsAggregationService,
		stateRootUpdateBlsAggregationService:   stateRootUpdateBlsAggregationService,
		operatorSetUpdateBlsAggregationService: operatorSetUpdateBlsAggregationService,
		msgDb:                                  msgDb,
		tasks:                                  make(map[coretypes.TaskIndex]taskmanager.CheckpointTask),
		taskResponses:                          make(map[coretypes.TaskIndex]map[eigentypes.TaskResponseDigest]messages.CheckpointTaskResponse),
		stateRootUpdates:                       make(map[coretypes.MessageDigest]messages.StateRootUpdateMessage),
		operatorSetUpdates:                     make(map[coretypes.MessageDigest]messages.OperatorSetUpdateMessage),
		aggregatorListener:                     &SelectiveAggregatorListener{},
	}

	if config.EnableMetrics {
		eigenMetrics := metrics.NewEigenMetrics(avsName, config.MetricsIpPortAddress, registry, logger)
		if err = agg.EnableMetrics(registry); err != nil {
			return nil, err
		}

		agg.metrics = eigenMetrics
		agg.registry = registry
	}

	agg.aggregatorListener.IncAggregatorInitializations()

	return agg, nil
}

func (agg *Aggregator) EnableMetrics(registry *prometheus.Registry) error {
	aggregatorListener, err := MakeAggregatorMetrics(registry)
	if err != nil {
		return err
	}
	agg.aggregatorListener = aggregatorListener

	if err = agg.msgDb.EnableMetrics(registry); err != nil {
		return err
	}

	return nil
}

func (agg *Aggregator) Start(ctx context.Context) error {
	agg.logger.Info("Starting aggregator.")

	if agg.metrics != nil {
		agg.metrics.Start(ctx, agg.registry)
	}

	ticker := time.NewTicker(agg.checkpointInterval)
	agg.logger.Info("Aggregator set to send new task", "interval", agg.checkpointInterval.String())
	defer ticker.Stop()

	broadcasterErrorChan := agg.rollupBroadcaster.GetErrorChan()
	for {
		select {
		case <-ctx.Done():
			return agg.Close()
		case blsAggServiceResp := <-agg.taskBlsAggregationService.GetResponseChannel():
			agg.logger.Info("Received response from taskBlsAggregationService", "blsAggServiceResp", blsAggServiceResp)
			go agg.sendAggregatedResponseToContract(blsAggServiceResp)
		case blsAggServiceResp := <-agg.stateRootUpdateBlsAggregationService.GetResponseChannel():
			agg.logger.Info("Received response from stateRootUpdateBlsAggregationService", "blsAggServiceResp", blsAggServiceResp)
			agg.handleStateRootUpdateReachedQuorum(blsAggServiceResp)
		case blsAggServiceResp := <-agg.operatorSetUpdateBlsAggregationService.GetResponseChannel():
			agg.logger.Info("Received response from operatorSetUpdateBlsAggregationService", "blsAggServiceResp", blsAggServiceResp)
			agg.handleOperatorSetUpdateReachedQuorum(ctx, blsAggServiceResp)
		case <-ticker.C:
			go agg.sendNewCheckpointTask()
		case err := <-broadcasterErrorChan:
			// TODO: proper error handling in all class
			agg.logger.Error("Received error from broadcaster", "err", err)
		}
	}
}

func (agg *Aggregator) Close() error {
	if err := agg.msgDb.Close(); err != nil {
		return err
	}

	agg.httpClient.Close()
	agg.wsClient.Close()

	agg.rollupBroadcaster.Close()

	return nil
}

func (agg *Aggregator) sendAggregatedResponseToContract(blsAggServiceResp blsagg.BlsAggregationServiceResponse) {
	if blsAggServiceResp.Err != nil {
		agg.aggregatorListener.IncErroredSubmissions()
		if strings.Contains(blsAggServiceResp.Err.Error(), "expired") {
			agg.aggregatorListener.IncExpiredTasks()
		}

		agg.logger.Error("BlsAggregationServiceResponse contains an error", "err", blsAggServiceResp.Err)
		return
	}

	agg.aggregatorListener.ObserveLastCheckpointTaskReferenceReceived(blsAggServiceResp.TaskIndex)

	agg.logger.Info("Threshold reached. Sending aggregated response onchain.",
		"taskIndex", blsAggServiceResp.TaskIndex,
	)
	agg.tasksLock.RLock()
	task := agg.tasks[blsAggServiceResp.TaskIndex]
	agg.tasksLock.RUnlock()
	agg.taskResponsesLock.RLock()
	taskResponse := agg.taskResponses[blsAggServiceResp.TaskIndex][blsAggServiceResp.TaskResponseDigest]
	agg.taskResponsesLock.RUnlock()

	aggregation, err := messages.NewMessageBlsAggregationFromServiceResponse(uint64(task.TaskCreatedBlock), blsAggServiceResp)
	if err != nil {
		agg.logger.Error("Aggregator failed to format aggregation", "err", err)
		return
	}

	agg.aggregatorListener.ObserveLastCheckpointTaskReferenceAggregated(blsAggServiceResp.TaskIndex)

	currentBlock, err := agg.httpClient.BlockNumber(context.Background())
	if err != nil {
		agg.logger.Error("Error getting current block number", "err", err)
		return
	}

	if uint64(task.TaskCreatedBlock) == currentBlock {
		agg.logger.Info("Waiting roughly a block before sending aggregated response...")
		time.Sleep(20 * time.Second)
	}

	_, err = agg.avsWriter.SendAggregatedResponse(context.Background(), task, taskResponse, aggregation)
	if err != nil {
		agg.logger.Error("Aggregator failed to respond to task", "err", err)
		return
	}
}

// sendNewCheckpointTask sends a new task to the task manager contract, and updates the Task dict struct
// with the information of operators opted into quorum 0 at the block of task creation.
func (agg *Aggregator) sendNewCheckpointTask() {
	blockNumber, err := agg.httpClient.BlockNumber(context.Background())
	if err != nil {
		agg.logger.Error("Failed to get block number", "err", err)
		return
	}

	block, err := agg.httpClient.BlockByNumber(context.Background(), big.NewInt(0).SetUint64(blockNumber))
	if err != nil {
		agg.logger.Error("Failed to get block", "err", err)
		return
	}

	lastCheckpointToTimestamp, err := agg.avsReader.GetLastCheckpointToTimestamp(context.Background())
	if err != nil {
		agg.logger.Error("Failed to get last checkpoint toTimestamp", "err", err)
		return
	}

	toTimestamp := block.Time() - uint64(types.MESSAGE_SUBMISSION_TIMEOUT.Seconds()) - uint64(types.MESSAGE_BLS_AGGREGATION_TIMEOUT.Seconds())
	fromTimestamp := lastCheckpointToTimestamp + 1
	if lastCheckpointToTimestamp == 0 {
		fromTimestamp = toTimestamp - uint64(agg.checkpointInterval.Seconds())
	}

	agg.logger.Info("Aggregator sending new task", "fromTimestamp", fromTimestamp, "toTimestamp", toTimestamp)
	// Send checkpoint to the task manager contract
	newTask, taskIndex, err := agg.avsWriter.SendNewCheckpointTask(context.Background(), fromTimestamp, toTimestamp, types.TASK_QUORUM_THRESHOLD, coretypes.QUORUM_NUMBERS)
	if err != nil {
		agg.logger.Error("Aggregator failed to send checkpoint", "err", err)
		return
	}

	agg.aggregatorListener.ObserveLastCheckpointReferenceSent(taskIndex)

	agg.tasksLock.Lock()
	agg.tasks[taskIndex] = newTask
	agg.tasksLock.Unlock()

	quorumThresholds := make([]eigentypes.QuorumThresholdPercentage, len(newTask.QuorumNumbers))
	for i, _ := range newTask.QuorumNumbers {
		quorumThresholds[i] = types.TASK_AGGREGATION_QUORUM_THRESHOLD
	}
	// TODO(samlaf): we use seconds for now, but we should ideally pass a blocknumber to the blsAggregationService
	// and it should monitor the chain and only expire the task aggregation once the chain has reached that block number.
	taskTimeToExpiry := taskChallengeWindowBlock * blockTimeSeconds
	err = agg.taskBlsAggregationService.InitializeNewTask(taskIndex, newTask.TaskCreatedBlock, core.ConvertBytesToQuorumNumbers(newTask.QuorumNumbers), quorumThresholds, taskTimeToExpiry)
	if err != nil {
		agg.logger.Error("Failed to initialize new task", "err", err)
		return
	}
}

func (agg *Aggregator) handleStateRootUpdateReachedQuorum(blsAggServiceResp types.MessageBlsAggregationServiceResponse) {
	agg.stateRootUpdatesLock.RLock()
	msg, ok := agg.stateRootUpdates[blsAggServiceResp.MessageDigest]
	agg.stateRootUpdatesLock.RUnlock()

	if blsAggServiceResp.Finished {
		defer func() {
			agg.stateRootUpdatesLock.Lock()
			delete(agg.stateRootUpdates, blsAggServiceResp.MessageDigest)
			agg.stateRootUpdatesLock.Unlock()
		}()
	}

	agg.aggregatorListener.ObserveLastStateRootUpdateReceived(msg.RollupId, msg.BlockHeight)

	if blsAggServiceResp.Err != nil {
		agg.aggregatorListener.IncErroredSubmissions()
		if errors.Is(blsAggServiceResp.Err, MessageExpiredError) {
			agg.aggregatorListener.IncExpiredMessages()
		}

		agg.logger.Error("Aggregator BLS service returned error", "err", blsAggServiceResp.Err)
		return
	}

	if !ok {
		agg.logger.Error("Aggregator could not find matching message")
		return
	}

	agg.aggregatorListener.ObserveLastStateRootUpdateAggregated(msg.RollupId, msg.BlockHeight)

	agg.logger.Info("Storing state root update", "digest", blsAggServiceResp.MessageDigest, "status", blsAggServiceResp.Status)

	err := agg.msgDb.StoreStateRootUpdate(msg)
	if err != nil {
		agg.logger.Error("Aggregator could not store message")
		return
	}
	err = agg.msgDb.StoreStateRootUpdateAggregation(msg, blsAggServiceResp.MessageBlsAggregation)
	if err != nil {
		agg.logger.Error("Aggregator could not store message aggregation")
		return
	}
}

func (agg *Aggregator) handleOperatorSetUpdateReachedQuorum(ctx context.Context, blsAggServiceResp types.MessageBlsAggregationServiceResponse) {
	agg.operatorSetUpdatesLock.RLock()
	msg, ok := agg.operatorSetUpdates[blsAggServiceResp.MessageDigest]
	agg.operatorSetUpdatesLock.RUnlock()

	if blsAggServiceResp.Finished {
		defer func() {
			agg.operatorSetUpdatesLock.Lock()
			delete(agg.operatorSetUpdates, blsAggServiceResp.MessageDigest)
			agg.operatorSetUpdatesLock.Unlock()

			if blsAggServiceResp.Err == nil {
				signatureInfo := blsAggServiceResp.ExtractBindingRollup()
				agg.rollupBroadcaster.BroadcastOperatorSetUpdate(ctx, msg, signatureInfo)
			}
		}()
	}

	agg.aggregatorListener.ObserveLastOperatorSetUpdateReceived(msg.Id)

	if blsAggServiceResp.Err != nil {
		agg.aggregatorListener.IncErroredSubmissions()
		if errors.Is(blsAggServiceResp.Err, MessageExpiredError) {
			agg.aggregatorListener.IncExpiredMessages()
		}

		agg.logger.Error("Aggregator BLS service returned error", "err", blsAggServiceResp.Err)
		return
	}

	if !ok {
		agg.logger.Error("Aggregator could not find matching message")
		return
	}

	agg.aggregatorListener.ObserveLastOperatorSetUpdateAggregated(msg.Id)

	agg.logger.Info("Storing operator set update", "digest", blsAggServiceResp.MessageDigest, "status", blsAggServiceResp.Status)

	err := agg.msgDb.StoreOperatorSetUpdate(msg)
	if err != nil {
		agg.logger.Error("Aggregator could not store message")
		return
	}
	err = agg.msgDb.StoreOperatorSetUpdateAggregation(msg, blsAggServiceResp.MessageBlsAggregation)
	if err != nil {
		agg.logger.Error("Aggregator could not store message aggregation")
		return
	}
}

func (agg *Aggregator) ProcessSignedCheckpointTaskResponse(signedCheckpointTaskResponse *messages.SignedCheckpointTaskResponse) error {
	taskIndex := signedCheckpointTaskResponse.TaskResponse.ReferenceTaskIndex
	taskResponseDigest, err := signedCheckpointTaskResponse.TaskResponse.Digest()
	if err != nil {
		agg.logger.Error("Failed to get task response digest", "err", err)
		return TaskResponseDigestError
	}

	err = agg.taskBlsAggregationService.ProcessNewSignature(
		context.Background(), taskIndex, taskResponseDigest,
		&signedCheckpointTaskResponse.BlsSignature, signedCheckpointTaskResponse.OperatorId,
	)
	if err != nil {
		return err
	}

	agg.taskResponsesLock.Lock()
	if _, ok := agg.taskResponses[taskIndex]; !ok {
		agg.taskResponses[taskIndex] = make(map[eigentypes.TaskResponseDigest]messages.CheckpointTaskResponse)
	}
	if _, ok := agg.taskResponses[taskIndex][taskResponseDigest]; !ok {
		agg.taskResponses[taskIndex][taskResponseDigest] = signedCheckpointTaskResponse.TaskResponse
	}
	agg.taskResponsesLock.Unlock()

	return nil
}

// Rpc request handlers
func (agg *Aggregator) ProcessSignedStateRootUpdateMessage(signedStateRootUpdateMessage *messages.SignedStateRootUpdateMessage) error {
	messageDigest, err := signedStateRootUpdateMessage.Message.Digest()
	if err != nil {
		agg.logger.Error("Failed to get message digest", "err", err)
		return DigestError
	}

	timestamp := signedStateRootUpdateMessage.Message.Timestamp
	err = agg.validateMessageTimestamp(timestamp)
	if err != nil {
		agg.logger.Error("Failed to validate message timestamp", "err", err, "timestamp", timestamp)
		return err
	}

	err = agg.stateRootUpdateBlsAggregationService.InitializeMessageIfNotExists(
		messageDigest,
		coretypes.QUORUM_NUMBERS,
		[]eigentypes.QuorumThresholdPercentage{types.MESSAGE_AGGREGATION_QUORUM_THRESHOLD},
		types.MESSAGE_TTL,
		types.MESSAGE_BLS_AGGREGATION_TIMEOUT,
		0,
	)
	if err != nil {
		return err
	}

	agg.stateRootUpdatesLock.Lock()
	agg.stateRootUpdates[messageDigest] = signedStateRootUpdateMessage.Message
	agg.stateRootUpdatesLock.Unlock()

	err = agg.stateRootUpdateBlsAggregationService.ProcessNewSignature(
		context.Background(), messageDigest,
		&signedStateRootUpdateMessage.BlsSignature, signedStateRootUpdateMessage.OperatorId,
	)
	return err
}

func (agg *Aggregator) ProcessSignedOperatorSetUpdateMessage(signedOperatorSetUpdateMessage *messages.SignedOperatorSetUpdateMessage) error {
	messageDigest, err := signedOperatorSetUpdateMessage.Message.Digest()
	if err != nil {
		agg.logger.Error("Failed to get message digest", "err", err)
		return DigestError
	}

	timestamp := signedOperatorSetUpdateMessage.Message.Timestamp
	err = agg.validateMessageTimestamp(timestamp)
	if err != nil {
		agg.logger.Error("Failed to validate message timestamp", "err", err, "timestamp", timestamp)
		return err
	}

	blockNumber, err := agg.avsReader.GetOperatorSetUpdateBlock(context.Background(), signedOperatorSetUpdateMessage.Message.Id)
	if err != nil {
		agg.logger.Error("Failed to get operator set update block", "err", err)
		return GetOperatorSetUpdateBlockError
	}

	err = agg.operatorSetUpdateBlsAggregationService.InitializeMessageIfNotExists(
		messageDigest,
		coretypes.QUORUM_NUMBERS,
		[]eigentypes.QuorumThresholdPercentage{types.MESSAGE_AGGREGATION_QUORUM_THRESHOLD},
		types.MESSAGE_TTL,
		types.MESSAGE_BLS_AGGREGATION_TIMEOUT,
		uint64(blockNumber)-1,
	)
	if err != nil {
		return err
	}

	agg.operatorSetUpdatesLock.Lock()
	agg.operatorSetUpdates[messageDigest] = signedOperatorSetUpdateMessage.Message
	agg.operatorSetUpdatesLock.Unlock()

	err = agg.operatorSetUpdateBlsAggregationService.ProcessNewSignature(
		context.Background(), messageDigest,
		&signedOperatorSetUpdateMessage.BlsSignature, signedOperatorSetUpdateMessage.OperatorId,
	)

	return err
}

func (agg *Aggregator) GetAggregatedCheckpointMessages(fromTimestamp, toTimestamp uint64) (*messages.CheckpointMessages, error) {
	checkpointMessages, err := agg.msgDb.FetchCheckpointMessages(fromTimestamp, toTimestamp)
	if err != nil {
		return nil, err
	}

	return checkpointMessages, nil
}

func (agg *Aggregator) GetRegistryCoordinatorAddress(reply *string) error {
	*reply = agg.config.SFFLRegistryCoordinatorAddr.String()
	return nil
}

// Rest requests
func (agg *Aggregator) GetStateRootUpdateAggregation(rollupId uint32, blockHeight uint64) (*types.GetStateRootUpdateAggregationResponse, error) {
	message, err := agg.msgDb.FetchStateRootUpdate(uint32(rollupId), blockHeight)
	if err != nil {
		return nil, StateRootUpdateNotFoundError
	}

	aggregation, err := agg.msgDb.FetchStateRootUpdateAggregation(uint32(rollupId), blockHeight)
	if err != nil {
		return nil, StateRootAggNotFoundError
	}

	return &types.GetStateRootUpdateAggregationResponse{
		Message:     *message,
		Aggregation: *aggregation,
	}, nil
}

func (agg *Aggregator) GetOperatorSetUpdateAggregation(id uint64) (*types.GetOperatorSetUpdateAggregationResponse, error) {
	message, err := agg.msgDb.FetchOperatorSetUpdate(id)
	if err != nil {
		return nil, OperatorSetNotFoundError
	}

	aggregation, err := agg.msgDb.FetchOperatorSetUpdateAggregation(id)
	if err != nil {
		return nil, OperatorAggNotFoundError
	}

	return &types.GetOperatorSetUpdateAggregationResponse{
		Message:     *message,
		Aggregation: *aggregation,
	}, nil
}

func (agg *Aggregator) GetCheckpointMessages(fromTimestamp, toTimestamp uint64) (*types.GetCheckpointMessagesResponse, error) {
	checkpointMessages, err := agg.msgDb.FetchCheckpointMessages(fromTimestamp, toTimestamp)
	if err != nil {
		return nil, CheckpointNotFoundError
	}

	return &types.GetCheckpointMessagesResponse{
		CheckpointMessages: *checkpointMessages,
	}, nil
}

func (agg *Aggregator) validateMessageTimestamp(messageTimestamp uint64) error {
	now := agg.clock.Now().Unix()
	timeoutInSeconds := types.MESSAGE_SUBMISSION_TIMEOUT.Seconds()

	// Prevent possible underflow (specially in testing)
	if uint64(now) > uint64(timeoutInSeconds) && messageTimestamp < uint64(now)-uint64(timeoutInSeconds) {
		return MessageExpiredError
	}

	return nil
}
