package document

import "testing"

func TestOpenGet(t *testing.T) {
	s := NewStore()
	s.Open("file:///a.sv", "systemverilog", 1, "module a; endmodule")

	doc, ok := s.Get("file:///a.sv")
	if !ok {
		t.Fatalf("expected document to be found")
	}
	if doc.Version != 1 || doc.Text != "module a; endmodule" || doc.LanguageID != "systemverilog" {
		t.Fatalf("unexpected document: %+v", doc)
	}
	if s.Len() != 1 {
		t.Fatalf("expected 1 document, got %d", s.Len())
	}
}

func TestApplyFullChange(t *testing.T) {
	s := NewStore()
	s.Open("file:///a.sv", "systemverilog", 1, "old")

	if !s.ApplyFullChange("file:///a.sv", 2, "new") {
		t.Fatalf("expected ApplyFullChange to find the document")
	}

	doc, _ := s.Get("file:///a.sv")
	if doc.Version != 2 || doc.Text != "new" {
		t.Fatalf("unexpected document after change: %+v", doc)
	}
}

func TestApplyFullChangeUnknownDocument(t *testing.T) {
	s := NewStore()
	if s.ApplyFullChange("file:///missing.sv", 2, "new") {
		t.Fatalf("expected ApplyFullChange to report the document as unknown")
	}
}

func TestClose(t *testing.T) {
	s := NewStore()
	s.Open("file:///a.sv", "systemverilog", 1, "text")
	s.Close("file:///a.sv")

	if _, ok := s.Get("file:///a.sv"); ok {
		t.Fatalf("expected document to be gone after Close")
	}
	if s.Len() != 0 {
		t.Fatalf("expected 0 documents, got %d", s.Len())
	}
}

func TestGetReturnsCopy(t *testing.T) {
	s := NewStore()
	s.Open("file:///a.sv", "systemverilog", 1, "text")

	doc, _ := s.Get("file:///a.sv")
	doc.Text = "mutated"

	fresh, _ := s.Get("file:///a.sv")
	if fresh.Text != "text" {
		t.Fatalf("Get should return an independent copy, got mutated state: %q", fresh.Text)
	}
}
