# cloud

Permissionless x402 payments and provider settlement

## Overview

Extend the wallet-native inference-payment path so a customer pays an independent provider in a stablecoin without registering with Hanzo or giving the provider custody of the wallet: independent x402 facilitators, signed exact prices, replay protection, verifiable settlement and receipt reconciliation. The existing Circle-based path can remain one facilitator; it must not become a mandatory intermediary. References: x402 (Apache-2.0), ERC-3009.

Acceptance: an agent receives HTTP 402, signs a p

## Installation

```bash
# Clone the repository
git clone https://github.com/hanzoai/cloud.git
cd cloud

# Install dependencies (if applicable)
pip install -r requirements.txt  # Python
# or
npm install  # Node.js
```

## Usage

```bash
# Run the project
python main.py  # Python
# or
node index.js  # Node.js
```

## Contributing

Contributions are welcome! Please follow these steps:

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/my-feature`)
3. Commit your changes (`git commit -m 'Add my feature'`)
4. Push to the branch (`git push origin feature/my-feature`)
5. Open a Pull Request

## License

See [LICENSE](LICENSE) for details.
