
-- Fix model name
-- https://github.com/hanzoai/cloud/issues/2688

UPDATE models
SET model_name = 'text-embedding-3-large'
WHERE id = 'clruwn76700020al7gp8e4g4l'