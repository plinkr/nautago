package nauta

import (
	"strings"

	"golang.org/x/net/html"
)

func attr(node *html.Node, name string) string {
	for _, a := range node.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

func findByID(node *html.Node, id string) *html.Node {
	if node.Type == html.ElementNode && attr(node, "id") == id {
		return node
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if found := findByID(child, id); found != nil {
			return found
		}
	}
	return nil
}

func findTag(node *html.Node, tag string) *html.Node {
	if node.Type == html.ElementNode && node.Data == tag {
		return node
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if found := findTag(child, tag); found != nil {
			return found
		}
	}
	return nil
}

func nthChild(node *html.Node, tag string, n int) *html.Node {
	seen := 0
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != html.ElementNode {
			continue
		}
		if tag != "" && child.Data != tag {
			continue
		}
		seen++
		if seen == n {
			return child
		}
	}
	return nil
}

// Un campo sin atributo value se envía vacío, que es lo que el portal espera de
// los inputs que él mismo rellena.
func formInputs(form *html.Node) map[string]string {
	inputs := map[string]string{}
	if form == nil {
		return inputs
	}

	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "input" {
			if name := attr(node, "name"); name != "" {
				inputs[name] = attr(node, "value")
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(form)
	return inputs
}

func formByID(doc *html.Node, id string) *html.Node {
	node := findByID(doc, id)
	if node == nil {
		return nil
	}
	if node.Data == "form" {
		return node
	}
	return findTag(node, "form")
}

func cellText(node *html.Node) string {
	return strings.Join(strings.Fields(textContent(node)), " ")
}

func textContent(node *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
			sb.WriteString(" ")
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return sb.String()
}

// html.Parse tolera fragmentos y marcado roto; solo falla si falla la lectura.
// Un árbol vacío deja que las consultas no encuentren nada en lugar de propagar
// un error.
func parseDocument(body string) *html.Node {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return &html.Node{Type: html.DocumentNode}
	}
	return doc
}
