package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/novaprotocolio/amm-bots/utils"
	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"
	"strconv"
	"strings"
	"sync"
	"time"
)

func NewNovaClient(privateKey string, baseToken string, quoteToken string, baseUrl string) *NovaClient {
	client := NovaClient{
		"",
		privateKey,
		quoteToken,
		baseToken,
		baseUrl,
		0,
		0,
		0,
		decimal.Zero,
	}
	_ = client.Init()
	return &client
}

type NovaClient struct {
	Address        string
	privateKey     string
	quoteToken     string
	baseToken      string
	baseUrl        string
	pricePrecision int
	priceDecimal   int
	amountDecimal  int
	minAmount      decimal.Decimal
}

func (client *NovaClient) getNovaSignature() string {
	novaAuthStr := fmt.Sprintf("NOVA-AUTHENTICATION@%d", time.Now().UnixNano()/int64(time.Millisecond))
	sig := utils.SignString(client.privateKey, novaAuthStr)
	novaSignature := fmt.Sprintf("%s#%s#%s", client.Address, novaAuthStr, sig)
	return novaSignature
}

func (client *NovaClient) TradingPair() string {
	return strings.ToUpper(client.baseToken) + "-" + strings.ToUpper(client.quoteToken)
}

func (client *NovaClient) get(path string, params []utils.KeyPair) (string, error) {
	novaAuthStr := client.getNovaSignature()
	return utils.Get(
		utils.JoinUrlPath(client.baseUrl, path),
		"",
		params,
		[]utils.KeyPair{
			{"Nova-Authentication", novaAuthStr},
			{"Content-Type", "application/json"},
		},
	)
}

func (client *NovaClient) post(path string, body string, params []utils.KeyPair) (string, error) {
	novaAuthStr := client.getNovaSignature()
	return utils.Post(
		utils.JoinUrlPath(client.baseUrl, path),
		body,
		params,
		[]utils.KeyPair{
			{"Nova-Authentication", novaAuthStr},
			{"Content-Type", "application/json"},
		},
	)
}

func (client *NovaClient) delete(path string, params []utils.KeyPair) (string, error) {
	novaAuthStr := client.getNovaSignature()
	return utils.Delete(
		utils.JoinUrlPath(client.baseUrl, path),
		"",
		params,
		[]utils.KeyPair{
			{"Nova-Authentication", novaAuthStr},
			{"Content-Type", "application/json"},
		},
	)
}

func (client *NovaClient) Init() error {
	address := utils.PrivateKeyToAddress(client.privateKey)
	client.Address = address
	var dataContainer INovaMarkets
	resp, err := client.get("markets", utils.EmptyKeyPairList)
	if err != nil {
		return err
	}
	_ = json.Unmarshal([]byte(resp), &dataContainer)
	if dataContainer.Desc != "success" {
		return errors.New(fmt.Sprintf("get market info failed %s", resp))
	}
	for _, market := range dataContainer.Data.Markets {
		if market.ID == client.TradingPair() {
			client.priceDecimal = market.PriceDecimals
			client.pricePrecision = market.PricePrecision
			client.amountDecimal = market.AmountDecimals
			minAmount, _ := decimal.NewFromString(market.MinOrderSize)
			client.minAmount = minAmount
		}
	}
	return nil
}

func (client *NovaClient) buildUnsignedOrder(
	price decimal.Decimal,
	amount decimal.Decimal,
	side string,
	orderType string,
	expireTimeInSecond int64) (string, error) {
	var dataContainer IBuildOrder
	var body = struct {
		MarketId  string          `json:"marketId"`
		Side      string          `json:"side"`
		OrderType string          `json:"orderType"`
		Price     decimal.Decimal `json:"price"`
		Amount    decimal.Decimal `json:"amount"`
		Expires   int64           `json:"expires"`
	}{client.TradingPair(), side, orderType, price, amount, expireTimeInSecond}
	bodyBytes, _ := json.Marshal(body)
	resp, err := client.post("orders/build", string(bodyBytes), utils.EmptyKeyPairList)
	if err != nil {
		return "", err
	}
	_ = json.Unmarshal([]byte(resp), &dataContainer)
	if dataContainer.Desc != "success" {
		return "", errors.New(resp)
	} else {
		return dataContainer.Data.Order.ID, nil
	}
}

func (client *NovaClient) placeOrder(orderId string) bool {
	signature := utils.SignOrderId(client.privateKey, orderId)
	var body = struct {
		OrderId   string `json:"orderID"`
		Signature string `json:"signature"`
	}{orderId, signature}
	bodyBytes, _ := json.Marshal(body)
	resp, err := client.post("orders", string(bodyBytes), utils.EmptyKeyPairList)
	if err != nil {
		return false
	}
	var dataContainer IPlaceOrder
	_ = json.Unmarshal([]byte(resp), &dataContainer)
	if dataContainer.Desc != "success" {
		return false
	} else {
		return true
	}
}

func (client *NovaClient) CreateOrder(
	price decimal.Decimal,
	amount decimal.Decimal,
	side string,
	orderType string,
	expireTimeInSecond int64) (string, error) {
	validPrice := utils.SetDecimal(utils.SetPrecision(price, client.pricePrecision), client.priceDecimal)
	validAmount := utils.SetDecimal(amount, client.amountDecimal)
	if validAmount.LessThan(client.minAmount) {
		return "", errors.New(fmt.Sprintf("Nova client %s create order amount %s less than min amount %s", client.TradingPair(), validAmount.String(), client.minAmount.String()))
	}
	orderId, err := client.buildUnsignedOrder(validPrice, validAmount, side, orderType, expireTimeInSecond)
	if err != nil {
		return "", err
	}
	placeSuccess := client.placeOrder(orderId)
	if placeSuccess {
		logrus.Infof("Nova client %s create order - price:%s amount:%s side:%s %s", client.TradingPair(), validPrice, validAmount, side, orderId)
		return orderId, nil
	} else {
		return "", errors.New(fmt.Sprintf("Nova client %s place order failed", client.TradingPair()))
	}
}

func (client *NovaClient) CancelOrder(orderId string) error {
	resp, err := client.delete("orders/"+orderId, utils.EmptyKeyPairList)
	if err != nil {
		return err
	}
	var dataContainer ICancelOrder
	_ = json.Unmarshal([]byte(resp), &dataContainer)
	if dataContainer.Desc != "success" {
		return errors.New(fmt.Sprintf("Nova client %s cancel order %s failed", client.TradingPair(), orderId))
	} else {
		logrus.Infof("Nova client %s cancel order %s succeed", client.TradingPair(), orderId)
		return nil
	}
}

func (client *NovaClient) CancelAllPendingOrders() (bool, error) {
	orders, err := client.GetAllPendingOrders()
	if err != nil {
		return false, err
	}
	var wg sync.WaitGroup
	for _, order := range orders {
		wg.Add(1)
		go func(id string) {
			_ = client.CancelOrder(id)
			wg.Done()
		}(order.Id)
	}
	wg.Wait()
	return true, nil
}

func (client *NovaClient) parseNovaOrderResp(orderInfo INovaOrderResp) StdOrder {
	var orderData = EmptyStdOrder
	orderData.Id = orderInfo.ID
	orderData.Amount, _ = decimal.NewFromString(orderInfo.Amount)
	orderData.AvailableAmount, _ = decimal.NewFromString(orderInfo.AvailableAmount)
	orderData.Price, _ = decimal.NewFromString(orderInfo.Price)
	pendingAmount, _ := decimal.NewFromString(orderInfo.PendingAmount)
	confirmedAmount, _ := decimal.NewFromString(orderInfo.ConfirmedAmount)
	orderData.FilledAmount = pendingAmount.Add(confirmedAmount)
	if orderData.AvailableAmount.IsZero() {
		orderData.Status = utils.ORDER_CLOSE
	} else {
		orderData.Status = utils.ORDER_OPEN
	}
	if orderInfo.Side == "sell" {
		orderData.Side = utils.SELL
	} else {
		orderData.Side = utils.BUY
	}
	return orderData
}

func (client *NovaClient) GetOrder(orderId string) (StdOrder, error) {
	orderData := EmptyStdOrder
	resp, err := client.get("orders/"+orderId, utils.EmptyKeyPairList)
	if err != nil {
		return orderData, err
	}
	var dataContainer IOrder
	_ = json.Unmarshal([]byte(resp), &dataContainer)
	if dataContainer.Desc != "success" {
		return orderData, errors.New(fmt.Sprintf("Nova client %s get order failed", client.TradingPair()))
	} else {
		orderData = client.parseNovaOrderResp(dataContainer.Data.Order)
		return orderData, nil
	}
}

func (client *NovaClient) GetAllPendingOrders() ([]StdOrder, error) {
	var allOrders = []StdOrder{}
	var pageNum = 0
	for true {
		resp, err := client.get("orders", []utils.KeyPair{
			{"marketID", client.TradingPair()},
			{"perPage", "100"},
			{"status", "pending"},
			{"page", strconv.Itoa(pageNum)},
		})
		if err != nil {
			return allOrders, err
		}
		var dataContainer IAllPendingOrders
		_ = json.Unmarshal([]byte(resp), &dataContainer)
		if dataContainer.Desc != "success" {
			return allOrders, errors.New(fmt.Sprintf("Nova client %s get all pending orders failed", client.TradingPair()))
		}
		for _, order := range dataContainer.Data.Orders {
			var tempOrder = client.parseNovaOrderResp(order)
			allOrders = append(allOrders, tempOrder)
		}
		if len(allOrders) >= dataContainer.Data.Count {
			break
		} else {
			pageNum += 1
		}
	}
	return allOrders, nil
}

func (client *NovaClient) GetTradingErc20() (baseErc20 *utils.ERC20, quoteErc20 *utils.ERC20, err error) {
	var dataContainer INovaMarkets
	resp, err := client.get("markets", utils.EmptyKeyPairList)
	if err != nil {
		return nil, nil, err
	}
	_ = json.Unmarshal([]byte(resp), &dataContainer)
	if dataContainer.Desc != "success" {
		return nil, nil, errors.New(fmt.Sprintf("unmarshal failed %s", resp))
	}
	for _, market := range dataContainer.Data.Markets {
		if market.ID == client.TradingPair() {
			baseToken := utils.ERC20{
				client.baseToken,
				market.BaseTokenAddress,
				market.BaseTokenDecimals,
				true,
			}
			quoteToken := utils.ERC20{
				client.quoteToken,
				market.QuoteTokenAddress,
				market.QuoteTokenDecimals,
				true,
			}
			return &baseToken, &quoteToken, nil
		}
	}
	return nil, nil, errors.New("market not found")
}
