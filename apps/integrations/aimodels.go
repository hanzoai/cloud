package integrations

// aimodels.go registers the model-provider and vector-store connectors an AI
// startup reaches for beyond the anthropic/openai keys already present: each is
// a customer-held key verified live against the provider's cheapest
// authenticated read (a models list, an account/key echo, or an index list).

func init() {
	register(&Provider{
		ID: "gemini", Name: "Google Gemini",
		Description: "Gemini models via API key.",
		Category:    "AI", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "gemini",
			origin:   constOrigin("GEMINI_API_BASE", "https://generativelanguage.googleapis.com"),
			path:     "/v1beta/models", place: queryAt, name: "key", minLen: 16,
		}),
	})

	register(&Provider{
		ID: "groq", Name: "Groq",
		Description: "Fast inference on open models.",
		Category:    "AI", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "groq",
			origin:   constOrigin("GROQ_API_BASE", "https://api.groq.com"),
			path:     "/openai/v1/models", place: bearer, minLen: 16,
		}),
	})

	register(&Provider{
		ID: "mistral", Name: "Mistral",
		Description: "Mistral models via API key.",
		Category:    "AI", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "mistral",
			origin:   constOrigin("MISTRAL_API_BASE", "https://api.mistral.ai"),
			path:     "/v1/models", place: bearer, minLen: 16,
		}),
	})

	register(&Provider{
		ID: "cohere", Name: "Cohere",
		Description: "Command models, embeddings, and rerank.",
		Category:    "AI", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "cohere",
			origin:   constOrigin("COHERE_API_BASE", "https://api.cohere.com"),
			// /v1/models 401s a bad key. NOT /v1/check-api-key: it returns HTTP 200
			// with {"valid":false} in the BODY, which a status-only gate accepts —
			// fail-open. Do not "helpfully" switch back.
			path: "/v1/models", place: bearer, minLen: 16,
		}),
	})

	register(&Provider{
		ID: "together", Name: "Together",
		Description: "Open models, fine-tuning, and inference.",
		Category:    "AI", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "together",
			origin:   constOrigin("TOGETHER_API_BASE", "https://api.together.xyz"),
			path:     "/v1/models", place: bearer, minLen: 16,
		}),
	})

	register(&Provider{
		ID: "replicate", Name: "Replicate",
		Description: "Run and host models.",
		Category:    "AI", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "replicate",
			origin:   constOrigin("REPLICATE_API_BASE", "https://api.replicate.com"),
			path:     "/v1/account", place: tokenAuth, minLen: 16,
		}),
	})

	register(&Provider{
		ID: "huggingface", Name: "Hugging Face",
		Description: "Models, datasets, and inference.",
		Category:    "AI", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "huggingface",
			origin:   constOrigin("HUGGINGFACE_API_BASE", "https://huggingface.co"),
			path:     "/api/whoami-v2", place: bearer, minLen: 8, prefix: "hf_",
		}),
	})

	register(&Provider{
		ID: "openrouter", Name: "OpenRouter",
		Description: "One key, many model providers.",
		Category:    "AI", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "openrouter",
			origin:   constOrigin("OPENROUTER_API_BASE", "https://openrouter.ai"),
			path:     "/api/v1/auth/key", place: bearer, minLen: 16,
		}),
	})

	register(&Provider{
		ID: "xai", Name: "xAI",
		Description: "Grok models via API key.",
		Category:    "AI", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "xai",
			origin:   constOrigin("XAI_API_BASE", "https://api.x.ai"),
			path:     "/v1/api-key", place: bearer, minLen: 16,
		}),
	})

	register(&Provider{
		ID: "fireworks", Name: "Fireworks",
		Description: "Fast inference on open models.",
		Category:    "AI", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "fireworks",
			origin:   constOrigin("FIREWORKS_API_BASE", "https://api.fireworks.ai"),
			path:     "/inference/v1/models", place: bearer, minLen: 16,
		}),
	})

	register(&Provider{
		ID: "deepseek", Name: "DeepSeek",
		Description: "DeepSeek models via API key.",
		Category:    "AI", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "deepseek",
			origin:   constOrigin("DEEPSEEK_API_BASE", "https://api.deepseek.com"),
			path:     "/user/balance", place: bearer, minLen: 16,
		}),
	})

	register(&Provider{
		ID: "pinecone", Name: "Pinecone",
		Description: "Managed vector database.",
		Category:    "Data", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "pinecone",
			origin:   constOrigin("PINECONE_API_BASE", "https://api.pinecone.io"),
			path:     "/indexes", place: headerAt, name: "Api-Key", minLen: 16,
			extra: map[string]string{"X-Pinecone-API-Version": "2024-10"},
		}),
	})
}
