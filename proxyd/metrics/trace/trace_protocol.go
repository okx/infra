package trace

import "strings"

const (
	RemoteAddrKey = "remote"
	LocalAddrKey  = "local"

	AllowAll                       = "*" // allow all
	DefaultMethodsWhiteListToTrace = "eth_sendTransaction, eth_sendRawTransaction, eth_getTransactionCount, eth_blockNumber, eth_getTransactionReceipt, eth_estimateGas, eth_call, eth_getBlockByHash, eth_newFilter, eth_getFilterChanges, eth_getLogs, eth_getCode, eth_getBalance, eth_getStorageAt, eth_blockByNumber, eth_getBlockReceipts, eth_transactionPreExec, eth_getInternalTransactions, eth_gasPrice, eth_getBlockGasLimit, eth_minGasPrice"
)

func ParseMethodsWhiteListToTrace(methods string) map[string]struct{} {
	ret := map[string]struct{}{}
	raw := strings.Split(methods, ",")
	for i := range raw {
		ret[strings.TrimSpace(raw[i])] = struct{}{}
	}
	return ret
}
