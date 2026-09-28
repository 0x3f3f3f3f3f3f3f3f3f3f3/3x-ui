package outbound

// Probe the submitted settings, not a stale template with the same tag.
// Unrelated broken entries must not poison isolated per-item retries.
func selectProbeOutbounds(items []*httpBatchItem, context []any) []any {
	requested := make(map[string]map[string]any, len(items))
	for _, item := range items {
		requested[item.tag] = item.outbound
	}
	outbounds := make([]any, 0, len(context)+len(items))
	for _, raw := range context {
		ob, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		tag, _ := ob["tag"].(string)
		if replacement := requested[tag]; replacement != nil {
			ob = replacement
		}
		outbounds = append(outbounds, ob)
	}
	for _, item := range items {
		if !outboundsContainTag(outbounds, item.tag) {
			outbounds = append(outbounds, item.outbound)
		}
	}
	byTag := make(map[string][]map[string]any, len(outbounds))
	for _, raw := range outbounds {
		ob := raw.(map[string]any)
		tag, _ := ob["tag"].(string)
		byTag[tag] = append(byTag[tag], ob)
	}
	wanted := make(map[string]bool)
	queue := make([]string, 0, len(items))
	for _, item := range items {
		queue = append(queue, item.tag)
	}
	for len(queue) > 0 {
		tag := queue[0]
		queue = queue[1:]
		if wanted[tag] {
			continue
		}
		wanted[tag] = true
		for _, ob := range byTag[tag] {
			stream, _ := ob["streamSettings"].(map[string]any)
			sockopt, _ := stream["sockopt"].(map[string]any)
			if next, _ := sockopt["dialerProxy"].(string); next != "" {
				queue = append(queue, next)
			}
			proxy, _ := ob["proxySettings"].(map[string]any)
			if next, _ := proxy["tag"].(string); next != "" {
				queue = append(queue, next)
			}
		}
	}
	selected := make([]any, 0, len(wanted))
	for _, raw := range outbounds {
		ob := raw.(map[string]any)
		if tag, _ := ob["tag"].(string); wanted[tag] {
			selected = append(selected, ob)
		}
	}
	return selected
}
