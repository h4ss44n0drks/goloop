# Genesis Transaction

## Introduction
This document specifies the genesis file format.

## Value Types

| Value type       | Description                 | Example                                      |
| ----------       | -----------                 | -------                                      |
| T_ADDR_EOA       | "hx" + 40-digit HEX string  | `"hxbe258ceb872e08851f1f59694dac2558708ece11"` |
| T_ADDR_SCORE     | "cx" + 40-digit HEX string  | `"cxb0776ee37f5b45bfaea8cff1d8232fbb6122ec32"` |
| T_HASH           | "0x" + 64-digit HEX string  | `"0xc71303ef8543d04b5dc1ba6579132b143087c68db1b2168786408fcbce568238"` |
| T_INT            | "0x" + lowercase HEX string | `"0xa"` |
| T_ARRAY          | json arrays                 | ` [ “0x1234567890”, “0x2345678990” ] ` |
| T_BOOLEAN        | "0x1" or "0x0"              | `"0x1"` |
| T_STRING         | json string                 | `"test string"` |
| T_BYTES          | "0x" + lowercase HEX string | `"0x112233445566..."` |
| T_DICT           | json dictionary             | `{ "supply": "0x1" }` |


## Params

* `accounts` (required, T_ARRAY) <br>
  It contains EOA(Externally Owned Account) or CA(Contract Account) information.
  determines who starts out with how much balance or which application is pre-installed when the block-chain starts
  
  * account (T_DICT)
    * `name` (T_STRING, default=`null`) <br>
      It's name the accounts. It is required to define the special account such as `god`,
      `treasury` or `governance`, otherwise it’s optional only for account alias.
    
    * `address` (T_ADDR_EOA or T_ADDR_SCORE) <br>
      account address
    
    * `balance` (T_INT, default=`"0x0"`) <br>
      Initial balance of the account.
    
    * `score` (T_DICT, default=`null`) <br>
      Required if the account(CA) has score to be pre-installed.
    
      * `owner` (T_ADDR_EOA) <br>
        address of this contract owner.
      
      * `contentType` (T_STRING) <br>
        MIME type of content. `application/zip` is usually used for user SCORE,
        while `application/x.score.system` is used for system SCORE.
      
      * `contentId` (T_STRING, replace `content`) <br>
        contentId is the content URI.
      
        | Prefix  | Description                      | Sample |
        | ------- | ------                           | -----  |
        | `hash:` | Used for hashable SCORE.         | `"hash:0x1234567890abcdef...e"` |
        | `cid:`  | Used for system SCORE.           | `"cid:multisig/r1"` |

      * `content` (T_BYTES, replace `contentId`) <br>
        Hex string contains bytes of compressed codes.
        
      * `params` (T_DICT, default=`null`) <br>
        Parameters will be passed to on_install()
        
* `chain` (T_DICT, default=`null`)

  * `auditEnabled` (T_BOOLEAN, default=`"0x0"`) <br>
    determines whether audit is required. Default is false.
    
  * `deployerWhiteListEnabled` (T_BOOLEAN, default=`"0x0"`) <br>
    determines whether only white-listed deployers can deploy scores. Default is false.
    
  * `fee` (T_DICT,default=`null`)
    * `stepPrice` (T_INT, default=`"0x0"`) <br>
       The price of one step. Fee is the multiplication of step price and steps used.
       
    * `stepLimit` (T_DICT, default=`null`) <br>
      Maximum step allowance for the request type. If it's not specified, both values
      are set as zero(`"0x0"`)
      * `invoke`  (T_INT)
      * `query` (T_INT)
      
    * `stepCosts` (T_DICT, default=`null`) <br>
      The cost of each step type. If it's not specified, all values
      are set as zero(`"0x0"`).
      * `default` (T_INT)
      * `contractCall` (T_INT)
      * `contractCreate` (T_INT)
      * `contractUpdatee` (T_INT)
      * `contractDestruct` (T_INT)
      * `contractSet` (T_INT)
      * `get` (T_INT)
      * `set` (T_INT)
      * `replace` (T_INT)
      * `delete` (T_INT)
      * `input` (T_INT)
      * `eventLog` (T_INT)
      * `apiCall` (T_INT)
      
  * `validatorList` (T_ARRAY, default=`[ ]`) <br>
     the list of addresses participating in the consensus.
     If it's empty, then it will not work.
     * validator (T_ADDR_EOA)
     
  * `memberList` (T_ARRAY, default=`[ ]`) <br>
    the list of addresses participating in the network.
    If it's empty, then it accepts all network connection.
    * member (T_ADDR_EOA)


## Example

```json
{
 "accounts": [
   {
     "name": "god",
     "address": "hxff9221db215ce1a511cbe0a12ff9eb70be4e5764",
     "balance": "0x2961fff8ca4a62327800000"
   },
   {
     "name": "treasury",
     "address": "hx1000000000000000000000000000000000000000",
     "balance": "0x0"
   },
   {
     "name:":  "governance",
     "address": "cx0000000000000000000000000000000000000001",
     "score": {
       "owner": "hx609c1c454528bae228514ceccec0c0939637a3fb",
       "contentType": "application/zip",
       "contentId": "hash:0x23cx01af5570f5a1810b7af78caf4bc70a660f0df51e42baf91d4de5b2328de0",
       "params": {
         "governor": "hx11cbe0a213e5a10e7926c4aa5943093f9221db2a"
       }
     }
   },
   {
     "name": "multisig",
     "address": "cx0000000000000000000000000000000000000002",
     "score":{
       "owner": "hx609c1c454528bae228514ceccec0c0939637a3fb",
       "contentType": "application/zip",
       "contentId": "hash:0x810b7af78caf4bc70a660f0df51e42baf91d4de5b2328de0e83dfc56fd70a6cb",
       "params": {
         "maxMember": "0x10"
       }
     }
   }
 ],
 "chain" : {
   "auditEnabled" : true,
   "deployerWhiteListEnabled" : false,
   "fee": {
     "stepPrice": "0x10000",
     "stepLimit": {
       "invoke": "0x9502f900",
       "query": "0x2faf080"
     },
     "stepCosts": {
       "default" : "0x186a0",
       "contractCall" : "0x61a8",
       "contractCreate":   "0x3b9aca00",
       "contractUpdate":   "0x5f5e1000",
       "contractDestruct": "0x11170",
       "contractSet":      "0x7530",
       "get":              "0x0",
       "set":              "0x140",
       "replace":          "0x50",
       "delete":           "-0xf0",
       "input":            "0xc8",
       "eventLog":         "0x64",
       "apiCall":          "0x0"
     }
   },
   "validatorList" : [
     "hx4805489d4fd3c07fea9b7e1b210e7926c4aa5943",
     "hx6903484805487fea9b8054c07fea9b7e54c07fef",
     "hx7fea9b7e54c5487fee54c543902f009aab312300",
     "hxdef10990388eeefab3827980e083e028f08f8aaa",
     "hxee01910d0f0a90b00de30999f099db9babd9e255"
   ]
 }
}
```
