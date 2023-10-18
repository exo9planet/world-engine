package ecs

import (
	"errors"
	"fmt"

	"pkg.world.dev/world-engine/cardinal/ecs/entity"
	"pkg.world.dev/world-engine/cardinal/ecs/filter"
	"pkg.world.dev/world-engine/cardinal/ecs/log"
	"pkg.world.dev/world-engine/cardinal/ecs/transaction"
)

// CreatePersonaTransaction allows for the associating of a persona tag with a signer address.
type CreatePersonaTransaction struct {
	PersonaTag    string `json:"personaTag"`
	SignerAddress string `json:"signerAddress"`
}

type CreatePersonaTransactionResult struct {
	Success bool `json:"success"`
}

// CreatePersonaTx is a concrete ECS transaction.
var CreatePersonaTx = NewTransactionType[CreatePersonaTransaction, CreatePersonaTransactionResult](
	"create-persona",
	WithTxEVMSupport[CreatePersonaTransaction, CreatePersonaTransactionResult],
)

type AuthorizePersonaAddress struct {
	PersonaTag string
	Address    string
}

type AuthorizePersonaAddressResult struct {
	Success bool
}

var AuthorizePersonaAddressTx = NewTransactionType[AuthorizePersonaAddress, AuthorizePersonaAddressResult](
	"authorize-persona-address",
)

// AuthorizePersonaAddressSystem enables users to authorize an address to a persona tag. This is mostly used so that
// users who want to interact with the game via smart contract can link their EVM address to their persona tag, enabling
// them to mutate their owned state from the context of the EVM.
func AuthorizePersonaAddressSystem(world *World, queue *transaction.TxQueue, _ *log.Logger) error {
	personaTagToAddress, err := buildPersonaTagMapping(world)
	if err != nil {
		return err
	}
	AuthorizePersonaAddressTx.ForEach(world, queue, func(tx TxData[AuthorizePersonaAddress]) (AuthorizePersonaAddressResult, error) {
		val, sig := tx.Value, tx.Sig
		result := AuthorizePersonaAddressResult{Success: false}
		if sig.PersonaTag != val.PersonaTag {
			return AuthorizePersonaAddressResult{Success: false}, fmt.Errorf("sigher does not match request")
		}
		data, ok := personaTagToAddress[tx.Value.PersonaTag]
		if !ok {
			return result, fmt.Errorf("persona does not exist")
		}
		err = UpdateComponent[SignerComponent](world, data.EntityID, func(s *SignerComponent) *SignerComponent {
			for _, addr := range s.AuthorizedAddresses {
				if addr == val.Address {
					return s
				}
			}
			s.AuthorizedAddresses = append(s.AuthorizedAddresses, val.Address)
			return s
		})
		if err != nil {
			return result, fmt.Errorf("unable to update signer component with address: %w", err)
		}
		result.Success = true
		return result, nil
	})
	return nil
}

type SignerComponent struct {
	PersonaTag          string
	SignerAddress       string
	AuthorizedAddresses []string
}

func (SignerComponent) Name() string {
	return "SignerComponent"
}

type personaTagComponentData struct {
	SignerAddress string
	EntityID      entity.ID
}

func buildPersonaTagMapping(world *World) (map[string]personaTagComponentData, error) {
	personaTagToAddress := map[string]personaTagComponentData{}
	var errs []error
	q, err := world.NewSearch(Exact(SignerComponent{}))
	c, err := world.GetComponentByName(SignerComponent{}.Name())
	if err != nil {
		return nil, err
	}
	filter.Exact(c)
	if err != nil {
		return nil, err
	}
	q.Each(world, func(id entity.ID) bool {
		sc, err := GetComponent[SignerComponent](world, id)
		if err != nil {
			errs = append(errs, err)
			return true
		}
		personaTagToAddress[sc.PersonaTag] = personaTagComponentData{
			SignerAddress: sc.SignerAddress,
			EntityID:      id,
		}
		return true
	})
	if len(errs) != 0 {
		return nil, errors.Join(errs...)
	}
	return personaTagToAddress, nil
}

// RegisterPersonaSystem is an ecs.System that will associate persona tags with signature addresses. Each persona tag
// may have at most 1 signer, so additional attempts to register a signer with a persona tag will be ignored.
func RegisterPersonaSystem(world *World, queue *transaction.TxQueue, _ *log.Logger) error {
	createTxs := CreatePersonaTx.In(queue)
	if len(createTxs) == 0 {
		return nil
	}
	personaTagToAddress, err := buildPersonaTagMapping(world)
	if err != nil {
		return err
	}
	for _, txData := range createTxs {
		tx := txData.Value
		if _, ok := personaTagToAddress[tx.PersonaTag]; ok {
			// This PersonaTag has already been registered. Don't do anything
			continue
		}
		id, err := Create(world, SignerComponent{})
		if err != nil {
			CreatePersonaTx.AddError(world, txData.TxHash, err)
			continue
		}
		if err := SetComponent[SignerComponent](world, id, &SignerComponent{
			PersonaTag:    tx.PersonaTag,
			SignerAddress: tx.SignerAddress,
		}); err != nil {
			CreatePersonaTx.AddError(world, txData.TxHash, err)
			continue
		}
		personaTagToAddress[tx.PersonaTag] = personaTagComponentData{
			SignerAddress: tx.SignerAddress,
			EntityID:      id,
		}
		CreatePersonaTx.SetResult(world, txData.TxHash, CreatePersonaTransactionResult{
			Success: true,
		})
	}

	return nil
}

var (
	ErrorPersonaTagHasNoSigner        = errors.New("persona tag does not have a signer")
	ErrorCreatePersonaTxsNotProcessed = errors.New("create persona txs have not been processed for the given tick")
)

// GetSignerForPersonaTag returns the signer address that has been registered for the given persona tag after the
// given tick. If the world's tick is less than or equal to the given tick, ErrorCreatePersonaTXsNotProcessed is returned.
// If the given personaTag has no signer address, ErrorPersonaTagHasNoSigner is returned.
func (w *World) GetSignerForPersonaTag(personaTag string, tick uint64) (addr string, err error) {
	if tick >= w.tick {
		return "", ErrorCreatePersonaTxsNotProcessed
	}
	var errs []error
	q, err := w.NewSearch(Exact(SignerComponent{}))
	if err != nil {
		return "", err
	}
	q.Each(w, func(id entity.ID) bool {
		sc, err := GetComponent[SignerComponent](w, id)
		//sc, err := SignerComp.Get(w, id)
		if err != nil {
			errs = append(errs, err)
		}
		if sc.PersonaTag == personaTag {
			addr = sc.SignerAddress
			return false
		}
		return true
	})
	if len(errs) > 0 {
		return "", errors.Join(errs...)
	}

	if addr == "" {
		return "", ErrorPersonaTagHasNoSigner
	}
	return addr, nil
}
